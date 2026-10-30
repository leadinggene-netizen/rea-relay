//+------------------------------------------------------------------+
//|  Hashing.mqh                                                     |
//|  Base64 / SHA-1 / HMAC-SHA256 helpers built on MQL5 CryptEncode. |
//|  No DLL imports — pure native crypto (TR-8).                     |
//+------------------------------------------------------------------+
#ifndef REA_HASHING_MQH
#define REA_HASHING_MQH

// Hex-encode a byte buffer (lowercase), matching Go's hex.EncodeToString so
// the HMAC the relay computes (internal/auth.Compute) compares equal.
string BytesToHex(const uchar &data[])
{
   string hex = "";
   for(int i = 0; i < ArraySize(data); i++)
      hex += StringFormat("%02x", data[i]);
   return hex;
}

// Append the raw bytes of a string (UTF-8, no terminator) to a byte array.
void AppendStringBytes(uchar &dst[], const string s)
{
   uchar tmp[];
   int n = StringToCharArray(s, tmp, 0, -1, CP_UTF8);
   // StringToCharArray adds a trailing 0; drop it.
   if(n > 0 && tmp[n - 1] == 0)
      n--;
   int base = ArraySize(dst);
   ArrayResize(dst, base + n);
   for(int i = 0; i < n; i++)
      dst[base + i] = tmp[i];
}

// SHA-1 of a buffer.
bool Sha1(const uchar &data[], uchar &out[])
{
   uchar key[];
   return CryptEncode(CRYPT_HASH_SHA1, data, key, out) > 0;
}

// SHA-256 of a buffer.
bool Sha256(const uchar &data[], uchar &out[])
{
   uchar key[];
   return CryptEncode(CRYPT_HASH_SHA256, data, key, out) > 0;
}

// Base64-encode a buffer into a string.
string Base64Encode(const uchar &data[])
{
   uchar key[];
   uchar enc[];
   if(CryptEncode(CRYPT_BASE64, data, key, enc) <= 0)
      return "";
   return CharArrayToString(enc, 0, WHOLE_ARRAY, CP_UTF8);
}

// HMAC-SHA256(secret, message) -> lowercase hex, matching internal/auth.Compute.
// Implemented from the FIPS 198 construction because MQL5 exposes no HMAC.
string HmacSha256Hex(const string secret, const uchar &message[])
{
   const int block = 64; // SHA-256 block size.
   uchar key[];
   AppendStringBytes(key, secret);
   // Keys longer than the block are hashed down first.
   if(ArraySize(key) > block)
   {
      uchar hk[];
      Sha256(key, hk);
      ArrayCopy(key, hk);
   }
   ArrayResize(key, block); // zero-pads on growth (MQL5 zero-fills new elements).
   // Note: if the key was shorter, the extra bytes are already zero.

   uchar ipad[], opad[];
   ArrayResize(ipad, block);
   ArrayResize(opad, block);
   for(int i = 0; i < block; i++)
   {
      ipad[i] = (uchar)(key[i] ^ 0x36);
      opad[i] = (uchar)(key[i] ^ 0x5c);
   }

   // inner = SHA256(ipad || message)
   uchar innerIn[];
   ArrayCopy(innerIn, ipad);
   int base = ArraySize(innerIn);
   ArrayResize(innerIn, base + ArraySize(message));
   for(int i = 0; i < ArraySize(message); i++)
      innerIn[base + i] = message[i];
   uchar innerHash[];
   Sha256(innerIn, innerHash);

   // outer = SHA256(opad || inner)
   uchar outerIn[];
   ArrayCopy(outerIn, opad);
   base = ArraySize(outerIn);
   ArrayResize(outerIn, base + ArraySize(innerHash));
   for(int i = 0; i < ArraySize(innerHash); i++)
      outerIn[base + i] = innerHash[i];
   uchar mac[];
   Sha256(outerIn, mac);

   return BytesToHex(mac);
}

// Compute the relay-compatible handshake signature:
//   HMAC-SHA256(secret, connectionKey + "\n" + ts + "\n" + nonce)  (hex)
string HandshakeSignature(const string secret, const string connKey,
                          const long ts, const string nonce)
{
   uchar msg[];
   AppendStringBytes(msg, connKey);
   AppendStringBytes(msg, "\n");
   AppendStringBytes(msg, IntegerToString(ts));
   AppendStringBytes(msg, "\n");
   AppendStringBytes(msg, nonce);
   return HmacSha256Hex(secret, msg);
}

#endif // REA_HASHING_MQH
