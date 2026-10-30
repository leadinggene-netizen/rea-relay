//+------------------------------------------------------------------+
//|  WsClient.mqh                                                    |
//|  RFC 6455 WebSocket client over native MQL5 TLS sockets          |
//|  (SocketConnect + SocketTlsHandshake + SocketTlsSend/Read).      |
//|  NO DLL imports, no "Allow DLL imports" (TR-8).                  |
//|                                                                  |
//|  Spec note: §13 TR-8 names "SocketTlsConnect"; the shipping      |
//|  MQL5 API realizes a TLS socket via SocketConnect followed by    |
//|  SocketTlsHandshake, then SocketTlsSend / SocketTlsRead. See     |
//|  ea/README.md.                                                   |
//+------------------------------------------------------------------+
#ifndef REA_WSCLIENT_MQH
#define REA_WSCLIENT_MQH

#include "Hashing.mqh"

#define WS_OP_TEXT   0x1
#define WS_OP_BINARY 0x2
#define WS_OP_CLOSE  0x8
#define WS_OP_PING   0x9
#define WS_OP_PONG   0xA

class CWsClient
{
private:
   int      m_sock;
   string   m_host;
   int      m_port;
   bool     m_connected;
   uchar    m_rxbuf[];   // leftover bytes carried across reads

   bool     TlsSendAll(const uchar &data[]);
   bool     TlsFill(const int want, const uint timeoutMs);
   bool     ReadExact(uchar &out[], const int n, const uint timeoutMs);

public:
            CWsClient(): m_sock(INVALID_HANDLE), m_port(443), m_connected(false) {}
           ~CWsClient() { Close(); }

   bool     IsConnected() const { return m_connected; }

   // Open a TLS socket and perform the RFC 6455 upgrade. The auth material
   // (connection_key, ts, nonce, sig, resume_token) is carried as query
   // parameters on the GET line, matching the relay's handshake (TR-2).
   bool     Connect(const string host, const int port, const string path);

   // Send one application text message as a single masked client frame.
   bool     SendText(const string msg);

   // Read the next application text message. Returns true and fills 'msg' when a
   // full text message arrived; answers ping with pong transparently. Returns
   // false on timeout (no complete message yet) or socket error (check
   // IsConnected()).
   bool     ReadText(string &msg, const uint timeoutMs);

   void     Close();
};

//+------------------------------------------------------------------+
bool CWsClient::Connect(const string host, const int port, const string path)
{
   Close();
   m_host = host;
   m_port = port;
   m_sock = SocketCreate();
   if(m_sock == INVALID_HANDLE)
      return false;
   if(!SocketConnect(m_sock, host, port, 5000))
   {
      Close();
      return false;
   }
   if(!SocketTlsHandshake(m_sock, host))
   {
      Close();
      return false;
   }

   // --- RFC 6455 client handshake ---
   uchar nonce[16];
   for(int i = 0; i < 16; i++)
      nonce[i] = (uchar)(MathRand() & 0xff);
   string key = Base64Encode(nonce);

   string req =
      "GET " + path + " HTTP/1.1\r\n" +
      "Host: " + host + "\r\n" +
      "Upgrade: websocket\r\n" +
      "Connection: Upgrade\r\n" +
      "Sec-WebSocket-Key: " + key + "\r\n" +
      "Sec-WebSocket-Version: 13\r\n\r\n";

   uchar reqb[];
   AppendStringBytes(reqb, req);
   if(!TlsSendAll(reqb))
   {
      Close();
      return false;
   }

   // Read the HTTP response headers up to the blank line.
   string resp = "";
   uint start = GetTickCount();
   while(StringFind(resp, "\r\n\r\n") < 0)
   {
      if(GetTickCount() - start > 5000)
      {
         Close();
         return false;
      }
      uchar b[];
      uint avail = SocketTlsReadAvailable(m_sock);
      if(avail == 0)
      {
         Sleep(10);
         continue;
      }
      int got = SocketTlsRead(m_sock, b, (int)avail, 1000);
      if(got <= 0)
         continue;
      resp += CharArrayToString(b, 0, got, CP_UTF8);
   }
   if(StringFind(resp, " 101 ") < 0)
   {
      Close();
      return false;
   }
   m_connected = true;
   return true;
}

//+------------------------------------------------------------------+
bool CWsClient::TlsSendAll(const uchar &data[])
{
   int total = ArraySize(data);
   int sent = 0;
   uchar chunk[];
   while(sent < total)
   {
      int n = total - sent;
      ArrayResize(chunk, n);
      for(int i = 0; i < n; i++)
         chunk[i] = data[sent + i];
      int w = SocketTlsSend(m_sock, chunk, n);
      if(w <= 0)
         return false;
      sent += w;
   }
   return true;
}

//+------------------------------------------------------------------+
// Ensure at least 'want' bytes are buffered in m_rxbuf (blocking up to timeout).
bool CWsClient::TlsFill(const int want, const uint timeoutMs)
{
   uint start = GetTickCount();
   while(ArraySize(m_rxbuf) < want)
   {
      if(GetTickCount() - start > timeoutMs)
         return false;
      uint avail = SocketTlsReadAvailable(m_sock);
      if(avail == 0)
      {
         if(!SocketIsConnected(m_sock))
         {
            m_connected = false;
            return false;
         }
         Sleep(5);
         continue;
      }
      uchar b[];
      int got = SocketTlsRead(m_sock, b, (int)avail, timeoutMs);
      if(got <= 0)
      {
         if(!SocketIsConnected(m_sock))
         {
            m_connected = false;
            return false;
         }
         continue;
      }
      int base = ArraySize(m_rxbuf);
      ArrayResize(m_rxbuf, base + got);
      for(int i = 0; i < got; i++)
         m_rxbuf[base + i] = b[i];
   }
   return true;
}

//+------------------------------------------------------------------+
// Pop exactly n bytes off the front of the rx buffer.
bool CWsClient::ReadExact(uchar &out[], const int n, const uint timeoutMs)
{
   if(!TlsFill(n, timeoutMs))
      return false;
   ArrayResize(out, n);
   for(int i = 0; i < n; i++)
      out[i] = m_rxbuf[i];
   int rem = ArraySize(m_rxbuf) - n;
   uchar tail[];
   ArrayResize(tail, rem);
   for(int i = 0; i < rem; i++)
      tail[i] = m_rxbuf[n + i];
   ArrayResize(m_rxbuf, rem);
   for(int i = 0; i < rem; i++)
      m_rxbuf[i] = tail[i];
   return true;
}

//+------------------------------------------------------------------+
bool CWsClient::SendText(const string msg)
{
   if(!m_connected)
      return false;
   uchar payload[];
   AppendStringBytes(payload, msg);
   int n = ArraySize(payload);

   uchar frame[];
   int hdr = 2;
   int lenField;
   if(n <= 125)        lenField = 0;
   else if(n <= 0xffff) lenField = 2;
   else                 lenField = 8;
   // 2 header bytes + extended length + 4-byte mask + payload
   ArrayResize(frame, 2 + lenField + 4 + n);

   frame[0] = (uchar)(0x80 | WS_OP_TEXT); // FIN + text
   int idx = 2;
   if(lenField == 0)
      frame[1] = (uchar)(0x80 | n);        // MASK + len
   else if(lenField == 2)
   {
      frame[1] = (uchar)(0x80 | 126);
      frame[2] = (uchar)((n >> 8) & 0xff);
      frame[3] = (uchar)(n & 0xff);
      idx = 4;
   }
   else
   {
      frame[1] = (uchar)(0x80 | 127);
      for(int i = 0; i < 8; i++)
         frame[2 + i] = (uchar)((n >> (8 * (7 - i))) & 0xff);
      idx = 10;
   }

   uchar mask[4];
   for(int i = 0; i < 4; i++)
   {
      mask[i] = (uchar)(MathRand() & 0xff);
      frame[idx + i] = mask[i];
   }
   int pstart = idx + 4;
   for(int i = 0; i < n; i++)
      frame[pstart + i] = (uchar)(payload[i] ^ mask[i & 3]);

   return TlsSendAll(frame);
}

//+------------------------------------------------------------------+
bool CWsClient::ReadText(string &msg, const uint timeoutMs)
{
   msg = "";
   string assembled = "";
   for(;;)
   {
      uchar h[];
      if(!ReadExact(h, 2, timeoutMs))
         return false;
      bool fin = (h[0] & 0x80) != 0;
      int opcode = h[0] & 0x0f;
      bool masked = (h[1] & 0x80) != 0; // server frames are not masked
      long len = h[1] & 0x7f;
      if(len == 126)
      {
         uchar e[];
         if(!ReadExact(e, 2, timeoutMs)) return false;
         len = ((long)e[0] << 8) | e[1];
      }
      else if(len == 127)
      {
         uchar e[];
         if(!ReadExact(e, 8, timeoutMs)) return false;
         len = 0;
         for(int i = 0; i < 8; i++)
            len = (len << 8) | e[i];
      }
      uchar mk[4];
      if(masked)
      {
         uchar m[];
         if(!ReadExact(m, 4, timeoutMs)) return false;
         for(int i = 0; i < 4; i++) mk[i] = m[i];
      }
      uchar payload[];
      if(len > 0)
      {
         if(!ReadExact(payload, (int)len, timeoutMs))
            return false;
         if(masked)
            for(int i = 0; i < (int)len; i++)
               payload[i] = (uchar)(payload[i] ^ mk[i & 3]);
      }

      if(opcode == WS_OP_PING)
      {
         // Echo a pong (control frames carry the ping payload).
         uchar frame[];
         ArrayResize(frame, 2 + 4 + (int)len);
         frame[0] = (uchar)(0x80 | WS_OP_PONG);
         frame[1] = (uchar)(0x80 | (int)len);
         uchar mask[4];
         for(int i = 0; i < 4; i++)
         {
            mask[i] = (uchar)(MathRand() & 0xff);
            frame[2 + i] = mask[i];
         }
         for(int i = 0; i < (int)len; i++)
            frame[6 + i] = (uchar)(payload[i] ^ mask[i & 3]);
         TlsSendAll(frame);
         continue;
      }
      if(opcode == WS_OP_CLOSE)
      {
         m_connected = false;
         return false;
      }
      if(opcode == WS_OP_PONG)
         continue;

      if(len > 0)
         assembled += CharArrayToString(payload, 0, (int)len, CP_UTF8);
      if(fin)
      {
         msg = assembled;
         return true;
      }
   }
}

//+------------------------------------------------------------------+
void CWsClient::Close()
{
   if(m_sock != INVALID_HANDLE)
   {
      SocketClose(m_sock);
      m_sock = INVALID_HANDLE;
   }
   m_connected = false;
   ArrayResize(m_rxbuf, 0);
}

#endif // REA_WSCLIENT_MQH
