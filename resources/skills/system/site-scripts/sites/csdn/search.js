/* @meta
{
  "name": "csdn/search",
  "description": "CSDN 技术文章搜索",
  "domain": "so.csdn.net",
  "args": {
    "query": {"required": true, "description": "Search query"},
    "page": {"required": false, "description": "Page number (default 1)"}
  },
  "readOnly": true,
  "example": "bb-browser site csdn/search \"Python\""
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query'};
  const page = parseInt(args.page) || 1;

  const url = 'https://so.csdn.net/api/v3/search?q=' + encodeURIComponent(args.query)
    + '&t=all&p=' + page
    + '&s=0&tm=0&lv=-1&ft=0&l=&u=&ct=-1&pnt=-1&ry=-1&ss=-1&dct=-1&vco=-1&cc=-1&sc=-1&ald=-1&ep=&wp=0';

  const resp = await fetch(url, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Make sure so.csdn.net is accessible'};

  const d = await resp.json();

  const strip = (html) => (html || '').replace(/<[^>]+>/g, '').replace(/&nbsp;/g, ' ')
    .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&').trim();

  const results = (d.result_vos || []).map((item, i) => ({
    rank: (page - 1) * 20 + i + 1,
    type: item.type,
    title: strip(item.title || ''),
    url: item.url || '',
    description: strip(item.description || item.body || '').substring(0, 300),
    author: item.nickname || item.author || '',
    views: parseInt(item.view) || 0,
    likes: parseInt(item.digg) || 0,
    collections: parseInt(item.collections) || 0,
    created: item.create_time ? new Date(parseInt(item.create_time)).toISOString() : null
  }));

  return {
    query: args.query,
    page: page,
    total: d.total || 0,
    count: results.length,
    results: results
  };
}
