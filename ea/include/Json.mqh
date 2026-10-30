//+------------------------------------------------------------------+
//|  Json.mqh                                                        |
//|  Minimal JSON helpers for the typed transport envelope (TR-3).   |
//|  A full JSON library is out of scope; this handles the flat,     |
//|  well-known envelope shape the relay emits and the agent sends.  |
//+------------------------------------------------------------------+
#ifndef REA_JSON_MQH
#define REA_JSON_MQH

// --- tiny extractor for the relay's flat envelope -----------------------

// Return the string value of "key" in a JSON object, or "" if absent.
string JsonStr(const string json, const string key)
{
   string pat = "\"" + key + "\"";
   int k = StringFind(json, pat);
   if(k < 0)
      return "";
   int colon = StringFind(json, ":", k + StringLen(pat));
   if(colon < 0)
      return "";
   int i = colon + 1;
   int n = StringLen(json);
   // skip whitespace
   while(i < n && (StringGetCharacter(json, i) == ' ' || StringGetCharacter(json, i) == '\t'))
      i++;
   if(i >= n || StringGetCharacter(json, i) != '"')
      return ""; // not a string value
   i++;
   string out = "";
   while(i < n)
   {
      ushort c = StringGetCharacter(json, i);
      if(c == '\\' && i + 1 < n)
      {
         ushort nx = StringGetCharacter(json, i + 1);
         if(nx == 'n') out += "\n";
         else if(nx == 't') out += "\t";
         else out += ShortToString(nx);
         i += 2;
         continue;
      }
      if(c == '"')
         break;
      out += ShortToString(c);
      i++;
   }
   return out;
}

// Return the numeric value of "key" as a double; def if absent.
double JsonNum(const string json, const string key, const double def)
{
   string pat = "\"" + key + "\"";
   int k = StringFind(json, pat);
   if(k < 0)
      return def;
   int colon = StringFind(json, ":", k + StringLen(pat));
   if(colon < 0)
      return def;
   int i = colon + 1;
   int n = StringLen(json);
   while(i < n && (StringGetCharacter(json, i) == ' ' || StringGetCharacter(json, i) == '\t'))
      i++;
   string num = "";
   while(i < n)
   {
      ushort c = StringGetCharacter(json, i);
      if((c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E')
      {
         num += ShortToString(c);
         i++;
         continue;
      }
      break;
   }
   if(num == "")
      return def;
   return StringToDouble(num);
}

long JsonInt(const string json, const string key, const long def)
{
   return (long)JsonNum(json, key, (double)def);
}

// Does "key" appear at all (to distinguish present-null / present-0 from absent)?
bool JsonHas(const string json, const string key)
{
   return StringFind(json, "\"" + key + "\"") >= 0;
}

// --- builders -----------------------------------------------------------

// Build an ack envelope.
string JsonAck(const long deliverySeq)
{
   return StringFormat("{\"type\":\"ack\",\"delivery_seq\":%I64d}", deliverySeq);
}

// Build a result envelope.
string JsonResult(const long deliverySeq, const string accountID,
                  const string status, const double finalVolume,
                  const string brokerCode, const string reasonCode)
{
   string s = StringFormat(
      "{\"type\":\"result\",\"delivery_seq\":%I64d,\"result\":{\"account_id\":\"%s\",\"status\":\"%s\",\"final_volume\":%s",
      deliverySeq, accountID, status, DoubleToString(finalVolume, 2));
   if(brokerCode != "")
      s += StringFormat(",\"broker_code\":\"%s\"", brokerCode);
   if(reasonCode != "")
      s += StringFormat(",\"reason\":\"%s\"", reasonCode);
   s += "}}";
   return s;
}

// Build a heartbeat envelope.
string JsonHeartbeat()
{
   return "{\"type\":\"heartbeat\"}";
}

#endif // REA_JSON_MQH
