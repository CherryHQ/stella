/* @meta
{
  "name": "xueqiu/hot-stock",
  "description": "获取雪球热门股票榜",
  "domain": "xueqiu.com",
  "args": {
    "count": {"required": false, "description": "返回数量，默认 20，最大 50"},
    "type": {"required": false, "description": "榜单类型：10=人气榜(默认) 12=关注榜"}
  },
  "readOnly": true,
  "example": "bb-browser site xueqiu/hot-stock 10"
}
*/

async function(args) {
  var count = Math.min(parseInt(args.count) || 20, 50);
  var type = args.type || '10';
  var resp = await fetch('https://stock.xueqiu.com/v5/stock/hot_stock/list.json?size=' + count + '&type=' + type, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  var d = await resp.json();
  if (!d.data || !d.data.items) return {error: '获取失败', raw: d};

  var items = d.data.items.map(function(s, i) {
    return {
      rank: i + 1,
      symbol: s.symbol,
      name: s.name,
      price: s.current,
      changePercent: s.percent != null ? s.percent.toFixed(2) + '%' : null,
      heat: s.value,
      rank_change: s.rank_change,
      url: 'https://xueqiu.com/S/' + s.symbol
    };
  });

  return {type: type === '12' ? '关注榜' : '人气榜', count: items.length, items: items};
}
