/* @meta
{
  "name": "bilibili/popular",
  "description": "Get Bilibili popular/trending videos",
  "domain": "www.bilibili.com",
  "args": {
    "count": {"required": false, "description": "Number of videos (default: 20, max: 50)"},
    "page": {"required": false, "description": "Page number (default: 1)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site bilibili/popular 10"
}
*/

async function(args) {
  const ps = Math.min(parseInt(args.count) || 20, 50);
  const pn = parseInt(args.page) || 1;
  const resp = await fetch('https://api.bilibili.com/x/web-interface/popular?ps=' + ps + '&pn=' + pn, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const d = await resp.json();
  if (d.code !== 0) return {error: d.message || 'API error ' + d.code, hint: 'Not logged in?'};
  const videos = (d.data?.list || []).map((v, i) => ({
    rank: (pn - 1) * ps + i + 1,
    bvid: v.bvid,
    title: v.title,
    author: v.owner?.name,
    author_mid: v.owner?.mid,
    cover: v.pic,
    duration: v.duration,
    view: v.stat?.view,
    like: v.stat?.like,
    danmaku: v.stat?.danmaku,
    reply: v.stat?.reply,
    favorite: v.stat?.favorite,
    coin: v.stat?.coin,
    share: v.stat?.share,
    category: v.tname,
    pub_date: v.pubdate ? new Date(v.pubdate * 1000).toISOString() : null,
    url: 'https://www.bilibili.com/video/' + v.bvid,
    reason: v.rcmd_reason?.content || null
  }));
  return {page: pn, count: videos.length, no_more: !!d.data?.no_more, videos};
}
