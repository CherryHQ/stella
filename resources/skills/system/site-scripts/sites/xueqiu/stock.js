/* @meta
{
  "name": "xueqiu/stock",
  "description": "获取雪球股票实时行情",
  "domain": "xueqiu.com",
  "args": {
    "symbol": {"required": true, "description": "股票代码，如 SH600519、SZ000858、AAPL、00700"}
  },
  "readOnly": true,
  "example": "bb-browser site xueqiu/stock SH600519"
}
*/

async function(args) {
  if (!args.symbol) return {error: 'Missing argument: symbol', hint: '请输入股票代码，如 SH600519'};

  var symbol = args.symbol.toUpperCase();
  var resp = await fetch('https://stock.xueqiu.com/v5/stock/batch/quote.json?symbol=' + encodeURIComponent(symbol), {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  var d = await resp.json();

  if (!d.data || !d.data.items || d.data.items.length === 0) return {error: '未找到股票: ' + symbol};

  function fmtAmount(v) {
    if (v == null) return null;
    if (Math.abs(v) >= 1e12) return (v / 1e12).toFixed(2) + '万亿';
    if (Math.abs(v) >= 1e8) return (v / 1e8).toFixed(2) + '亿';
    if (Math.abs(v) >= 1e4) return (v / 1e4).toFixed(2) + '万';
    return v.toString();
  }

  var item = d.data.items[0];
  var q = item.quote || {};
  var m = item.market || {};

  return {
    name: q.name,
    symbol: q.symbol,
    exchange: q.exchange,
    currency: q.currency,
    price: q.current,
    change: q.chg,
    changePercent: q.percent != null ? q.percent.toFixed(2) + '%' : null,
    open: q.open,
    high: q.high,
    low: q.low,
    prevClose: q.last_close,
    amplitude: q.amplitude != null ? q.amplitude.toFixed(2) + '%' : null,
    volume: q.volume,
    amount: fmtAmount(q.amount),
    turnover_rate: q.turnover_rate != null ? q.turnover_rate.toFixed(2) + '%' : null,
    marketCap: fmtAmount(q.market_capital),
    floatMarketCap: fmtAmount(q.float_market_capital),
    ytdPercent: q.current_year_percent != null ? q.current_year_percent.toFixed(2) + '%' : null,
    market_status: m.status || null,
    time: q.timestamp ? new Date(q.timestamp).toISOString() : null,
    url: 'https://xueqiu.com/S/' + q.symbol
  };
}
