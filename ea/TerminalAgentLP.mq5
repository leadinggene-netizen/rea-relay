//+------------------------------------------------------------------+
//|  TerminalAgentLP.mq5                                            |
//|  Long-poll terminal agent (spec TR-7) using MT5 WebRequest()     |
//|  (the Windows TLS stack) instead of the raw-socket WebSocket.    |
//|                                                                  |
//|  Use this agent when SocketTlsHandshake fails (err 5274) — MT5's |
//|  raw-socket TLS is unreliable on many builds, while WebRequest   |
//|  uses the OS TLS stack (the same one curl/your browser use).     |
//|                                                                  |
//|  REQUIRES: the relay host added under                           |
//|  Tools > Options > Expert Advisors > Allow WebRequest for URL.   |
//|  Reuses TradeExec (FR-12..16), Json and Hashing from the WS EA.  |
//|  NOT compiled in CI — compile in MetaEditor, test on a demo.     |
//+------------------------------------------------------------------+
#property copyright "rea-relay"
#property version   "1.0"
#property strict

#include "include/Json.mqh"
#include "include/TradeExec.mqh"
#include "include/Hashing.mqh"

input string InpHost          = "relay.mt5tradebot.com"; // relay host
input string InpPath          = "/agent/longpoll";       // long-poll endpoint (TR-7)
input string InpConnectionKey = "demo-connection-key";   // per-connection key (FR-2)
input string InpSecret        = "";                       // shared secret; empty = no signature (FR-3)
input int    InpPollMs        = 1000;                     // poll interval in ms
input long   InpMagic         = 770001;                   // EA magic number

CTradeExec g_exec;
long       g_resumeToken = 0;     // highest delivery_seq completed (resume/ack)
string     g_seenKeys[];          // agent-side idempotency dedup (FR-8)

//+------------------------------------------------------------------+
// Build the endpoint URL with auth query parameters. The signature, when a
// secret is set, is HMAC-SHA256 over connection_key\nts\nnonce (matching the
// relay's internal/auth.Compute). resume_token carries the last completed
// delivery sequence so the relay acks/drops accordingly.
string BaseUrl()
{
   string url = "https://" + InpHost + InpPath + "?connection_key=" + InpConnectionKey +
                "&resume_token=" + IntegerToString(g_resumeToken);
   if(InpSecret != "")
   {
      long ts = (long)TimeGMT();
      string nonce = IntegerToString(GetTickCount()) + IntegerToString(MathRand());
      string sig = HandshakeSignature(InpSecret, InpConnectionKey, ts, nonce);
      url += "&ts=" + IntegerToString(ts) + "&nonce=" + nonce + "&sig=" + sig;
   }
   return url;
}

//+------------------------------------------------------------------+
// Minimal JSON array splitter: extract each top-level {...} object from a
// response body like [{..},{..}]. String-aware (skips braces inside strings).
int SplitJsonArray(const string body, string &out[])
{
   ArrayResize(out, 0);
   int n = StringLen(body), depth = 0, objStart = -1;
   bool inStr = false;
   for(int i = 0; i < n; i++)
   {
      ushort c = StringGetCharacter(body, i);
      if(inStr)
      {
         if(c == '\\') { i++; continue; } // skip escaped char
         if(c == '"') inStr = false;
         continue;
      }
      if(c == '"') { inStr = true; continue; }
      if(c == '{') { if(depth == 0) objStart = i; depth++; }
      else if(c == '}')
      {
         depth--;
         if(depth == 0 && objStart >= 0)
         {
            int sz = ArraySize(out);
            ArrayResize(out, sz + 1);
            out[sz] = StringSubstr(body, objStart, i - objStart + 1);
            objStart = -1;
         }
      }
   }
   return ArraySize(out);
}

//+------------------------------------------------------------------+
// Decode an `order` envelope into a ResolvedOrder (keys scanned flat; the Json
// helpers match a key anywhere in the object).
ResolvedOrder DecodeOrder(const string msg)
{
   ResolvedOrder o;
   o.deliverySeq      = JsonInt(msg, "delivery_seq", 0);
   o.instructionID    = JsonStr(msg, "instruction_id");
   o.idempotencyKey   = JsonStr(msg, "idempotency_key");
   o.accountID        = JsonStr(msg, "account_id");
   o.action           = JsonStr(msg, "action");
   o.brokerSymbol     = JsonStr(msg, "broker_symbol");
   o.lots             = JsonNum(msg, "lots", 0.0);
   o.hasSL            = JsonHas(msg, "sl_price");
   o.slPrice          = JsonNum(msg, "sl_price", 0.0);
   o.hasTP            = JsonHas(msg, "tp_price");
   o.tpPrice          = JsonNum(msg, "tp_price", 0.0);
   o.hasPending       = JsonHas(msg, "pending_price");
   o.pendingPrice     = JsonNum(msg, "pending_price", 0.0);
   o.hasPartial       = JsonHas(msg, "partial_fraction");
   o.partialFraction  = (int)JsonInt(msg, "partial_fraction", 0);
   o.hasCloseVolume   = JsonHas(msg, "close_volume");
   o.closeVolume      = JsonNum(msg, "close_volume", 0.0);
   o.comment          = JsonStr(msg, "comment");
   o.hasMaxSpread     = JsonHas(msg, "max_spread");
   o.maxSpread        = JsonNum(msg, "max_spread", 0.0);
   o.hasAccountFilter = JsonHas(msg, "account_filter");
   o.accountFilter    = JsonNum(msg, "account_filter", 0.0);
   return o;
}

bool AlreadyHandled(const string key)
{
   if(key == "") return false;
   for(int i = 0; i < ArraySize(g_seenKeys); i++)
      if(g_seenKeys[i] == key) return true;
   int n = ArraySize(g_seenKeys);
   ArrayResize(g_seenKeys, n + 1);
   g_seenKeys[n] = key;
   return false;
}

//+------------------------------------------------------------------+
// POST one result/ack envelope back to the relay.
void ReportResult(const string json)
{
   char data[], result[];
   string rhdr;
   int n = StringToCharArray(json, data, 0, WHOLE_ARRAY, CP_UTF8);
   if(n > 0) ArrayResize(data, n - 1);   // drop trailing NUL so the body is exact
   ResetLastError();
   int code = WebRequest("POST", BaseUrl(), "Content-Type: application/json\r\n",
                         5000, data, result, rhdr);
   if(code == -1)
      PrintFormat("LP: result POST failed err=%d", GetLastError());
}

//+------------------------------------------------------------------+
void PollOnce()
{
   char post[], result[];
   string rhdr;
   ResetLastError();
   int code = WebRequest("GET", BaseUrl(), "", 5000, post, result, rhdr);
   if(code == -1)
   {
      int err = GetLastError();
      PrintFormat("LP: poll GET failed err=%d%s", err,
                  (err == 4060 ? " (4060=add host to Allow WebRequest list)" : ""));
      return;
   }
   if(code == 204) return;                 // no orders waiting
   if(code != 200)
   {
      PrintFormat("LP: poll HTTP %d body=%s", code,
                  CharArrayToString(result, 0, WHOLE_ARRAY, CP_UTF8));
      return;
   }
   string body = CharArrayToString(result, 0, WHOLE_ARRAY, CP_UTF8);
   string envs[];
   int cnt = SplitJsonArray(body, envs);
   for(int i = 0; i < cnt; i++)
   {
      if(JsonStr(envs[i], "type") != "order") continue;
      ResolvedOrder o = DecodeOrder(envs[i]);
      ExecResult r;
      if(AlreadyHandled(o.idempotencyKey))
      {
         r.status = "filled"; r.finalVolume = 0.0; r.reasonCode = "duplicate_suppressed";
      }
      else
      {
         r = g_exec.Execute(o);
      }
      ReportResult(JsonResult(o.deliverySeq, o.accountID, r.status, r.finalVolume, r.brokerCode, r.reasonCode));
      if(o.deliverySeq > g_resumeToken) g_resumeToken = o.deliverySeq;
      PrintFormat("LP: seq=%I64d action=%s symbol=%s -> status=%s vol=%.2f %s",
                  o.deliverySeq, o.action, o.brokerSymbol, r.status, r.finalVolume, r.brokerCode);
   }
}

//+------------------------------------------------------------------+
int OnInit()
{
   g_exec.SetMagic(InpMagic);
   MathSrand((int)GetTickCount());
   PrintFormat("LP agent starting -> %s%s (key=%s)", InpHost, InpPath, InpConnectionKey);
   // one-shot reachability probe via the same WebRequest path
   char post[], result[]; string rhdr;
   ResetLastError();
   int code = WebRequest("GET", "https://" + InpHost + "/healthz", "", 5000, post, result, rhdr);
   if(code == -1)
      PrintFormat("LP: WARNING /healthz WebRequest failed err=%d (check Allow WebRequest list)", GetLastError());
   else
      PrintFormat("LP: relay reachable, /healthz HTTP %d body=[%s]", code,
                  CharArrayToString(result, 0, WHOLE_ARRAY, CP_UTF8));
   EventSetMillisecondTimer(InpPollMs);
   return INIT_SUCCEEDED;
}

void OnDeinit(const int reason) { EventKillTimer(); }
void OnTimer() { PollOnce(); }
//+------------------------------------------------------------------+
