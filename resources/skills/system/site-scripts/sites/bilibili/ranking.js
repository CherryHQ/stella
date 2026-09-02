/* @meta
{
  "name": "bilibili/ranking",
  "description": "Get Bilibili top ranking videos (all categories or specific category)",
  "domain": "www.bilibili.com",
  "args": {
    "count": {"required": false, "description": "Number of videos to return (default: 20, max: 100)"},
    "category": {"required": false, "description": "Category ID (rid): 0=all, 1=anime, 3=music, 4=game, 5=dance, 36=knowledge, 188=tech, 160=life, 211=food, 217=animal, 119=kichiku, 155=fashion, 202=info, 165=ad, 234=sports, 223=car, 177=documentary, 181=movie, 11=tv (default: 0)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site bilibili/ranking --category 36"
}
*/

async function(args) {
  const count = Math.min(parseInt(args.count) || 20, 100);
  const rid = parseInt(args.category) || 0;
  const resp = await fetch('https://api.bilibili.com/x/web-interface/ranking/v2?rid=' + rid + '&type=all', {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const d = await resp.json();
  if (d.code !== 0) return {error: d.message || 'API error ' + d.code, hint: 'Not logged in?'};
  const categoryNames = {0:'all',1:'anime',3:'music',4:'game',5:'dance',36:'knowledge',188:'tech',160:'life',211:'food',217:'animal',119:'kichiku',155:'fashion',202:'info',165:'ad',234:'sports',223:'car',177:'documentary',181:'movie',11:'tv'};
  const videos = (d.data?.list || []).slice(0, count).map((v, i) => ({
    rank: i + 1,
    bvid: v.bvid,
    title: v.title,
    author: v.owner?.name,
    author_mid: v.owner?.mid,
    cover: v.pic,
    duration: v.duration,
    view: v.stat?.view,
    like: v.stat?.like,
    danmaku: v.stat?.danmaku,
    coin: v.stat?.coin,
    favorite: v.stat?.favorite,
    category: v.tname,
    pub_date: v.pubdate ? new Date(v.pubdate * 1000).toISOString() : null,
    url: 'https://www.bilibili.com/video/' + v.bvid
  }));
  return {category: categoryNames[rid] || String(rid), count: videos.length, videos};
}
