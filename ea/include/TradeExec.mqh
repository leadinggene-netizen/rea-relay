//+------------------------------------------------------------------+
//|  TradeExec.mqh                                                   |
//|  Executes fully-resolved orders on the terminal (FR-12..16).     |
//|  The relay has already done all risk/volume math (FR-7): this    |
//|  module fills the given volume and absolute SL/TP, applies broker |
//|  min/max + lot-step, and the spread & account pre-trade gates.   |
//+------------------------------------------------------------------+
#ifndef REA_TRADEEXEC_MQH
#define REA_TRADEEXEC_MQH

#include <Trade/Trade.mqh>
#include <Trade/PositionInfo.mqh>

// One resolved order decoded from an `order` envelope.
struct ResolvedOrder
{
   long     deliverySeq;
   string   instructionID;
   string   idempotencyKey;
   string   accountID;
   string   action;        // open_long, close_short_partial, ...
   string   brokerSymbol;
   double   lots;
   bool     hasSL;
   double   slPrice;
   bool     hasTP;
   double   tpPrice;
   bool     hasPending;
   double   pendingPrice;
   bool     hasPartial;
   int      partialFraction; // 25 / 34 / 50
   bool     hasCloseVolume;
   double   closeVolume;
   string   comment;
   bool     hasMaxSpread;
   double   maxSpread;       // in points
   bool     hasAccountFilter;
   double   accountFilter;   // min equity
};

// Outcome reported back to the relay (FR-16).
struct ExecResult
{
   string   status;       // filled | partial | rejected | skipped | no_op
   double   finalVolume;
   string   brokerCode;   // broker retcode when rejected
   string   reasonCode;   // §8 reason code when skipped/rejected
};

class CTradeExec
{
private:
   CTrade         m_trade;
   CPositionInfo  m_pos;

   double   NormalizeVolume(const string sym, double vol, string &reasonOut, bool &outOfRange);
   bool     PassesGates(const ResolvedOrder &o, ExecResult &res);
   ExecResult OpenMarket(const ResolvedOrder &o, bool isLong);
   ExecResult OpenPending(const ResolvedOrder &o, bool isLong);
   ExecResult ModifyStops(const ResolvedOrder &o, bool isLong);
   ExecResult CloseSide(const ResolvedOrder &o, bool isLong, bool partial, bool byVolume);
   ExecResult CloseAll(const ResolvedOrder &o);
   ExecResult FlipTo(const ResolvedOrder &o, bool isLong);

public:
            CTradeExec() {}
   void     SetMagic(const long magic) { m_trade.SetExpertMagicNumber(magic); }
   ExecResult Execute(const ResolvedOrder &o);
};

//+------------------------------------------------------------------+
// Clamp to broker min/max (reject if out of range, EC-4/D-4) and round to the
// broker lot step (round-half-up, D-3). Server already rounded (FR-7/D-4); this
// is a defensive broker-side guard (FR-14).
double CTradeExec::NormalizeVolume(const string sym, double vol, string &reasonOut, bool &outOfRange)
{
   outOfRange = false;
   double vmin = SymbolInfoDouble(sym, SYMBOL_VOLUME_MIN);
   double vmax = SymbolInfoDouble(sym, SYMBOL_VOLUME_MAX);
   double vstep = SymbolInfoDouble(sym, SYMBOL_VOLUME_STEP);
   if(vstep <= 0.0)
      vstep = 0.01;
   if(vol < vmin - 1e-9 || vol > vmax + 1e-9)
   {
      outOfRange = true;
      reasonOut = "volume_out_of_range";
      return vol;
   }
   double steps = MathFloor(vol / vstep + 0.5); // round-half-up
   double rounded = steps * vstep;
   // Snap to the step's decimal precision to avoid FP drift.
   int digits = (int)MathRound(-MathLog10(vstep));
   if(digits < 0) digits = 0;
   return NormalizeDouble(rounded, digits);
}

//+------------------------------------------------------------------+
// Pre-trade gates: spread (FR-15/EC-6) and account_filter equity (FR-15/EC-7).
// Evaluated once per entry (D-5).
bool CTradeExec::PassesGates(const ResolvedOrder &o, ExecResult &res)
{
   if(o.hasMaxSpread)
   {
      double spreadPts = (double)SymbolInfoInteger(o.brokerSymbol, SYMBOL_SPREAD);
      if(spreadPts > o.maxSpread)
      {
         res.status = "skipped";
         res.reasonCode = "spread_exceeded";
         return false;
      }
   }
   if(o.hasAccountFilter)
   {
      double equity = AccountInfoDouble(ACCOUNT_EQUITY);
      if(equity < o.accountFilter)
      {
         res.status = "skipped";
         res.reasonCode = "account_filter_blocked";
         return false;
      }
   }
   return true;
}

//+------------------------------------------------------------------+
ExecResult CTradeExec::OpenMarket(const ResolvedOrder &o, bool isLong)
{
   ExecResult res;
   res.status = "rejected";
   res.finalVolume = 0.0;
   if(!PassesGates(o, res))
      return res;

   string rsn;
   bool oor;
   double vol = NormalizeVolume(o.brokerSymbol, o.lots, rsn, oor);
   if(oor)
   {
      res.reasonCode = rsn;
      return res;
   }

   double sl = o.hasSL ? o.slPrice : 0.0;
   double tp = o.hasTP ? o.tpPrice : 0.0;
   bool ok;
   if(isLong)
      ok = m_trade.Buy(vol, o.brokerSymbol, 0.0, sl, tp, o.comment);
   else
      ok = m_trade.Sell(vol, o.brokerSymbol, 0.0, sl, tp, o.comment);

   if(ok && m_trade.ResultRetcode() == TRADE_RETCODE_DONE)
   {
      res.status = "filled";
      res.finalVolume = vol;
   }
   else
   {
      res.status = "rejected";
      res.brokerCode = IntegerToString(m_trade.ResultRetcode());
      res.reasonCode = "broker_rejected";
   }
   return res;
}

//+------------------------------------------------------------------+
ExecResult CTradeExec::OpenPending(const ResolvedOrder &o, bool isLong)
{
   ExecResult res;
   res.status = "rejected";
   if(!PassesGates(o, res))
      return res;
   if(!o.hasPending)
   {
      res.reasonCode = "malformed_payload";
      return res;
   }
   string rsn;
   bool oor;
   double vol = NormalizeVolume(o.brokerSymbol, o.lots, rsn, oor);
   if(oor)
   {
      res.reasonCode = rsn;
      return res;
   }
   double sl = o.hasSL ? o.slPrice : 0.0;
   double tp = o.hasTP ? o.tpPrice : 0.0;
   double ask = SymbolInfoDouble(o.brokerSymbol, SYMBOL_ASK);
   double bid = SymbolInfoDouble(o.brokerSymbol, SYMBOL_BID);
   bool ok;
   // Limit vs stop is chosen from the pending price relative to the market.
   if(isLong)
      ok = (o.pendingPrice < ask)
           ? m_trade.BuyLimit(vol, o.pendingPrice, o.brokerSymbol, sl, tp, ORDER_TIME_GTC, 0, o.comment)
           : m_trade.BuyStop(vol, o.pendingPrice, o.brokerSymbol, sl, tp, ORDER_TIME_GTC, 0, o.comment);
   else
      ok = (o.pendingPrice > bid)
           ? m_trade.SellLimit(vol, o.pendingPrice, o.brokerSymbol, sl, tp, ORDER_TIME_GTC, 0, o.comment)
           : m_trade.SellStop(vol, o.pendingPrice, o.brokerSymbol, sl, tp, ORDER_TIME_GTC, 0, o.comment);

   if(ok && m_trade.ResultRetcode() == TRADE_RETCODE_DONE)
   {
      res.status = "filled"; // order placed
      res.finalVolume = vol;
   }
   else
   {
      res.status = "rejected";
      res.brokerCode = IntegerToString(m_trade.ResultRetcode());
      res.reasonCode = "broker_rejected";
   }
   return res;
}

//+------------------------------------------------------------------+
// Modify SL/TP of all positions on this symbol for the given side. A stop of
// zero means "move SL to breakeven" (FR-12): SL is set to the position's open
// price.
ExecResult CTradeExec::ModifyStops(const ResolvedOrder &o, bool isLong)
{
   ExecResult res;
   res.status = "no_op";
   res.reasonCode = "no_matching_position";
   long wantType = isLong ? POSITION_TYPE_BUY : POSITION_TYPE_SELL;
   bool any = false;
   for(int i = PositionsTotal() - 1; i >= 0; i--)
   {
      if(!m_pos.SelectByIndex(i))
         continue;
      if(m_pos.Symbol() != o.brokerSymbol || (long)m_pos.PositionType() != wantType)
         continue;
      double sl = o.hasSL ? o.slPrice : 0.0;
      if(o.hasSL && o.slPrice == 0.0)
         sl = m_pos.PriceOpen(); // breakeven
      double tp = o.hasTP ? o.tpPrice : m_pos.TakeProfit();
      if(m_trade.PositionModify(m_pos.Ticket(), sl, tp))
         any = true;
   }
   if(any)
   {
      res.status = "filled";
      res.reasonCode = "";
   }
   return res;
}

//+------------------------------------------------------------------+
// Close a side: full, partial-by-fraction (of initial volume), or by volume.
// close_all is handled separately. EC-10 no-op when nothing matches; EC-11 a
// fraction below broker min closes fully.
ExecResult CTradeExec::CloseSide(const ResolvedOrder &o, bool isLong, bool partial, bool byVolume)
{
   ExecResult res;
   res.status = "no_op";
   res.reasonCode = "no_matching_position";
   long wantType = isLong ? POSITION_TYPE_BUY : POSITION_TYPE_SELL;
   double vmin = SymbolInfoDouble(o.brokerSymbol, SYMBOL_VOLUME_MIN);
   double closedTotal = 0.0;
   bool any = false;
   bool anyPartial = false;

   for(int i = PositionsTotal() - 1; i >= 0; i--)
   {
      if(!m_pos.SelectByIndex(i))
         continue;
      if(m_pos.Symbol() != o.brokerSymbol || (long)m_pos.PositionType() != wantType)
         continue;
      double open = m_pos.Volume();
      double toClose = open;
      if(partial && o.hasPartial)
      {
         toClose = open * (double)o.partialFraction / 100.0;
         // EC-11: a partial below broker min closes fully.
         if(toClose < vmin - 1e-9)
            toClose = open;
         else
            anyPartial = true;
      }
      else if(byVolume && o.hasCloseVolume)
      {
         toClose = MathMin(o.closeVolume, open);
         if(toClose < open - 1e-9)
            anyPartial = true;
      }
      if(toClose >= open - 1e-9)
      {
         if(m_trade.PositionClose(m_pos.Ticket()))
         {
            any = true;
            closedTotal += open;
         }
      }
      else
      {
         if(m_trade.PositionClosePartial(m_pos.Ticket(), toClose))
         {
            any = true;
            closedTotal += toClose;
         }
      }
   }
   if(any)
   {
      res.status = anyPartial ? "partial" : "filled";
      res.finalVolume = closedTotal;
      res.reasonCode = "";
   }
   return res;
}

//+------------------------------------------------------------------+
ExecResult CTradeExec::CloseAll(const ResolvedOrder &o)
{
   ExecResult res;
   res.status = "no_op";
   res.reasonCode = "no_matching_position";
   double closedTotal = 0.0;
   bool any = false;
   for(int i = PositionsTotal() - 1; i >= 0; i--)
   {
      if(!m_pos.SelectByIndex(i))
         continue;
      if(m_pos.Symbol() != o.brokerSymbol)
         continue;
      double open = m_pos.Volume();
      if(m_trade.PositionClose(m_pos.Ticket()))
      {
         any = true;
         closedTotal += open;
      }
   }
   if(any)
   {
      res.status = "filled";
      res.finalVolume = closedTotal;
      res.reasonCode = "";
   }
   return res;
}

//+------------------------------------------------------------------+
ExecResult CTradeExec::Execute(const ResolvedOrder &o)
{
   if(o.action == "open_long")          return OpenMarket(o, true);
   if(o.action == "open_short")         return OpenMarket(o, false);
   if(o.action == "open_long_pending")  return OpenPending(o, true);
   if(o.action == "open_short_pending") return OpenPending(o, false);
   if(o.action == "flip_long")          return FlipTo(o, true);
   if(o.action == "flip_short")         return FlipTo(o, false);
   if(o.action == "modify_stops_long")  return ModifyStops(o, true);
   if(o.action == "modify_stops_short") return ModifyStops(o, false);
   if(o.action == "close_long")         return CloseSide(o, true,  false, false);
   if(o.action == "close_short")        return CloseSide(o, false, false, false);
   if(o.action == "close_all")          return CloseAll(o);
   if(o.action == "close_long_partial") return CloseSide(o, true,  true,  false);
   if(o.action == "close_short_partial")return CloseSide(o, false, true,  false);
   if(o.action == "close_long_volume")  return CloseSide(o, true,  false, true);
   if(o.action == "close_short_volume") return CloseSide(o, false, false, true);

   ExecResult res;
   res.status = "no_op";
   res.reasonCode = "no_matching_position";
   return res;
}

//+------------------------------------------------------------------+
// FlipTo closes any open position on the opposite side of the symbol, then
// opens a fresh market position on the requested side. Closing the opposite
// side is best-effort (a no-op if nothing is open); the open is the reported
// outcome, so the EA returns a clean fill even on the first signal.
ExecResult CTradeExec::FlipTo(const ResolvedOrder &o, const bool isLong)
{
   CloseSide(o, !isLong, false, false);   // flip_long closes shorts; flip_short closes longs
   return OpenMarket(o, isLong);
}

#endif // REA_TRADEEXEC_MQH
