/* @meta
{
  "name": "xueqiu/search",
  "description": "搜索雪球股票（代码或名称）",
  "domain": "xueqiu.com",
  "args": {
    "query": {"required": true, "description": "搜索关键词，如 茅台、AAPL、腾讯"},
    "count": {"required": false, "description": "返回数量，默认 10"}
  },
  "readOnly": true,
  "example": "bb-browser site xueqiu/search 茅台"
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query'};
  var count = Math.min(parseInt(args.count) || 10, 20);
  var resp = await fetch('https://xueqiu.com/stock/search.json?code=' + encodeURIComponent(args.query) + '&size=' + count, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  var d = await resp.json();
  var stocks = (d.stocks || []).map(function(s) {
    var symbol = '';
    if (s.exchange === 'SH' || s.exchange === 'SZ' || s.exchange === 'BJ') {
      symbol = s.code.startsWith(s.exchange) ? s.code : s.exchange + s.code;
    } else {
      symbol = s.code;
    }
    return {
      symbol: symbol,
      name: s.name,
      exchange: s.exchange,
      price: s.current,
      changePercent: s.percentage != null ? s.percentage.toFixed(2) + '%' : null,
      url: 'https://xueqiu.com/S/' + symbol
    };
  });
  return {keyword: args.query, count: stocks.length, results: stocks};
}
