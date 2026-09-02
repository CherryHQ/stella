/* @meta
{
  "name": "bilibili/trending",
  "description": "Get Bilibili trending search keywords (hot searches)",
  "domain": "www.bilibili.com",
  "args": {
    "count": {"required": false, "description": "Number of trending items (default: 20, max: 50)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site bilibili/trending"
}
*/

async function(args) {
  const count = Math.min(parseInt(args.count) || 20, 50);
  const resp = await fetch('https://api.bilibili.com/x/web-interface/wbi/search/square?limit=' + count, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const d = await resp.json();
  if (d.code !== 0) return {error: d.message || 'API error ' + d.code, hint: 'Not logged in?'};

  const items = (d.data?.trending?.list || []).slice(0, count).map((item, i) => ({
    rank: i + 1,
    keyword: item.keyword,
    show_name: item.show_name,
    is_hot: !!item.icon,
    icon: item.icon || null,
    search_url: 'https://search.bilibili.com/all?keyword=' + encodeURIComponent(item.keyword)
  }));

  return {count: items.length, items};
}
