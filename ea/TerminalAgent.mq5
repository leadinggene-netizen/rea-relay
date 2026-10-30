//+------------------------------------------------------------------+
//|  TerminalAgent.mq5                                              |
//|  Reference terminal agent for the rea-relay transport (TR-8).   |
//|                                                                  |
//|  Connects to the relay's WebSocket endpoint over a native MQL5  |
//|  TLS socket (NO DLL, no "Allow DLL imports"), authenticates per |
//|  TR-2, receives `order` messages, executes them (FR-12..16),    |
//|  replies with `ack` + `result`, sends heartbeats, and           |
//|  reconnects with resume (TR-4/TR-6).                            |
//|                                                                  |
//|  NOT COMPILED OR TESTED in the build container — compile in      |
//|  MetaEditor and test in a terminal. See ea/README.md.           |
//+------------------------------------------------------------------+
#property copyright "rea-relay"
#property version   "1.0"
#property strict

#include "include/WsClient.mqh"
#include "include/Json.mqh"
#include "include/TradeExec.mqh"
#include "include/Hashing.mqh"

input string InpHost          = "relay.example.com"; // relay host
input int    InpPort          = 443;                  // WSS port
input string InpPath          = "/agent/ws";          // WS endpoint path
input string InpConnectionKey = "replace_me";         // per-connection key (FR-2)
input string InpSecret        = "";                   // shared secret; empty = no signature (FR-3)
input int    InpHeartbeatSec  = 15;                   // heartbeat interval (TR-5)
input long   InpMagic         = 770001;               // EA magic number

CWsClient   g_ws;
CTradeExec  g_exec;
long        g_resumeToken = 0;      // highest delivery_seq completed (TR-4)
string      g_sessionID   = "";
datetime    g_lastBeat    = 0;
int         g_backoff     = 1;      // reconnect backoff seconds (TR-6)

//+------------------------------------------------------------------+
// Build the handshake path with auth query parameters (TR-2). The signature,
// when a secret is set, is HMAC-SHA256 over connection_key\nts\nnonce (matching
// the relay's internal/auth.Compute). resume_token carries the last completed
// delivery sequence so the relay can replay unacked orders.
string BuildPath()
{
   string path = InpPath + "?connection_key=" + InpConnectionKey +
                 "&resume_token=" + IntegerToString(g_resumeToken);
   if(InpSecret != "")
   {
      long ts = (long)TimeGMT();
      string nonce = IntegerToString(GetTickCount()) + IntegerToString(MathRand());
      string sig = HandshakeSignature(InpSecret, InpConnectionKey, ts, nonce);
      path += "&ts=" + IntegerToString(ts) + "&nonce=" + nonce + "&sig=" + sig;
   }
   return path;
}

//+------------------------------------------------------------------+
bool ConnectAndNegotiate()
{
   if(!g_ws.Connect(InpHost, InpPort, BuildPath()))
   {
      PrintFormat("connect/handshake failed to %s:%d", InpHost, InpPort);
      return false;
   }
   // Expect a welcome envelope carrying session_id + resume_token (TR-2).
   string msg;
   if(!g_ws.ReadText(msg, 5000))
   {
      Print("no welcome from relay");
      g_ws.Close();
      return false;
   }
   string type = JsonStr(msg, "type");
   if(type == "error")
   {
      PrintFormat("relay rejected handshake: %s", JsonStr(msg, "code"));
      g_ws.Close();
      return false;
   }
   if(type != "welcome")
   {
      PrintFormat("unexpected first message: %s", msg);
      g_ws.Close();
      return false;
   }
   g_sessionID   = JsonStr(msg, "session_id");
   g_resumeToken = JsonInt(msg, "resume_token", g_resumeToken);
   g_lastBeat    = TimeGMT();
   g_backoff     = 1;
   PrintFormat("session %s established (resume_token=%I64d)", g_sessionID, g_resumeToken);
   return true;
}

//+------------------------------------------------------------------+
// Decode an `order` envelope into a ResolvedOrder.
ResolvedOrder DecodeOrder(const string msg)
{
   ResolvedOrder o;
   o.deliverySeq    = JsonInt(msg, "delivery_seq", 0);
   o.instructionID  = JsonStr(msg, "instruction_id");
   o.idempotencyKey = JsonStr(msg, "idempotency_key");
   o.accountID      = JsonStr(msg, "account_id");
   o.action         = JsonStr(msg, "action");
   o.brokerSymbol   = JsonStr(msg, "broker_symbol");
   o.lots           = JsonNum(msg, "lots", 0.0);
   o.hasSL          = JsonHas(msg, "sl_price");
   o.slPrice        = JsonNum(msg, "sl_price", 0.0);
   o.hasTP          = JsonHas(msg, "tp_price");
   o.tpPrice        = JsonNum(msg, "tp_price", 0.0);
   o.hasPending     = JsonHas(msg, "pending_price");
   o.pendingPrice   = JsonNum(msg, "pending_price", 0.0);
   o.hasPartial     = JsonHas(msg, "partial_fraction");
   o.partialFraction= (int)JsonInt(msg, "partial_fraction", 0);
   o.hasCloseVolume = JsonHas(msg, "close_volume");
   o.closeVolume    = JsonNum(msg, "close_volume", 0.0);
   o.comment        = JsonStr(msg, "comment");
   o.hasMaxSpread   = JsonHas(msg, "max_spread");
   o.maxSpread      = JsonNum(msg, "max_spread", 0.0);
   o.hasAccountFilter = JsonHas(msg, "account_filter");
   o.accountFilter  = JsonNum(msg, "account_filter", 0.0);
   return o;
}

//+------------------------------------------------------------------+
// Idempotency guard (agent-side dedup, FR-8): track handled idempotency keys so
// a redelivered order executes at most once.
string g_seenKeys[];
bool AlreadyHandled(const string key)
{
   if(key == "")
      return false;
   for(int i = 0; i < ArraySize(g_seenKeys); i++)
      if(g_seenKeys[i] == key)
         return true;
   int n = ArraySize(g_seenKeys);
   ArrayResize(g_seenKeys, n + 1);
   g_seenKeys[n] = key;
   return false;
}

//+------------------------------------------------------------------+
void HandleOrder(const string msg)
{
   ResolvedOrder o = DecodeOrder(msg);

   // Ack receipt first (TR-3).
   g_ws.SendText(JsonAck(o.deliverySeq));

   ExecResult r;
   if(AlreadyHandled(o.idempotencyKey))
   {
      // Already executed on a prior delivery: report the dedup as a no-op fill
      // so the relay can advance its resume token without re-trading.
      r.status = "filled";
      r.finalVolume = 0.0;
      r.reasonCode = "duplicate_suppressed";
   }
   else
   {
      r = g_exec.Execute(o);
   }

   g_ws.SendText(JsonResult(o.deliverySeq, o.accountID, r.status, r.finalVolume, r.brokerCode, r.reasonCode));

   if(o.deliverySeq > g_resumeToken)
      g_resumeToken = o.deliverySeq;
}

//+------------------------------------------------------------------+
void PumpMessages()
{
   string msg;
   // Drain whatever is available without blocking the terminal.
   while(g_ws.IsConnected() && g_ws.ReadText(msg, 50))
   {
      string type = JsonStr(msg, "type");
      if(type == "order")
         HandleOrder(msg);
      else if(type == "pong")
         g_lastBeat = TimeGMT();
      else if(type == "control")
      {
         // pause/resume are enforced server-side; nothing to execute here.
      }
      else if(type == "error")
         PrintFormat("relay error: %s", JsonStr(msg, "code"));
   }
}

//+------------------------------------------------------------------+
int OnInit()
{
   g_exec.SetMagic(InpMagic);
   MathSrand((int)GetTickCount());
   if(!ConnectAndNegotiate())
      PrintFormat("initial connect failed; will retry on timer");
   EventSetTimer(1);
   return INIT_SUCCEEDED;
}

//+------------------------------------------------------------------+
void OnDeinit(const int reason)
{
   EventKillTimer();
   g_ws.Close();
}

//+------------------------------------------------------------------+
void OnTimer()
{
   if(!g_ws.IsConnected())
   {
      // Reconnect with exponential backoff + resume (TR-6).
      static datetime nextTry = 0;
      if(TimeGMT() < nextTry)
         return;
      if(!ConnectAndNegotiate())
      {
         g_backoff = MathMin(g_backoff * 2, 60);
         nextTry = TimeGMT() + g_backoff;
      }
      return;
   }

   PumpMessages();

   // Heartbeat (TR-5).
   if(TimeGMT() - g_lastBeat >= InpHeartbeatSec)
   {
      g_ws.SendText(JsonHeartbeat());
      g_lastBeat = TimeGMT();
   }
}
//+------------------------------------------------------------------+
