/* @meta
{
  "name": "bilibili/comments",
  "description": "Get comments for a Bilibili video",
  "domain": "www.bilibili.com",
  "args": {
    "bvid": {"required": true, "description": "Video BV ID"},
    "page": {"required": false, "description": "Page number (default: 1)"},
    "count": {"required": false, "description": "Comments per page (default: 20, max: 30)"},
    "sort": {"required": false, "description": "Sort: 0=by_time, 2=by_likes (default: 2)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site bilibili/comments BV1LGwHzrE4A"
}
*/

async function(args) {
  const bvid = args.bvid || args._positional?.[0];
  if (!bvid) return {error: 'Missing argument: bvid'};
  const pn = parseInt(args.page) || 1;
  const ps = Math.min(parseInt(args.count) || 20, 30);
  const sort = args.sort !== undefined ? parseInt(args.sort) : 2;

  // First get aid from bvid
  const viewResp = await fetch('https://api.bilibili.com/x/web-interface/view?bvid=' + encodeURIComponent(bvid), {credentials: 'include'});
  if (!viewResp.ok) return {error: 'HTTP ' + viewResp.status, hint: 'Not logged in?'};
  const viewData = await viewResp.json();
  if (viewData.code !== 0) return {error: viewData.message || 'Failed to get video info', hint: viewData.code === -404 ? 'Video not found' : 'Not logged in?'};
  const aid = viewData.data?.aid;

  // Fetch comments using aid
  const resp = await fetch('https://api.bilibili.com/x/v2/reply?type=1&oid=' + aid + '&pn=' + pn + '&ps=' + ps + '&sort=' + sort, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const d = await resp.json();
  if (d.code !== 0) return {error: d.message || 'API error ' + d.code, hint: 'Not logged in?'};

  const formatReply = r => ({
    rpid: r.rpid_str,
    user: r.member?.uname,
    user_mid: r.mid,
    user_level: r.member?.level_info?.current_level,
    content: r.content?.message,
    like: r.like,
    reply_count: r.rcount,
    time: r.ctime ? new Date(r.ctime * 1000).toISOString() : null,
    sub_replies: (r.replies || []).slice(0, 3).map(sr => ({
      user: sr.member?.uname,
      content: sr.content?.message,
      like: sr.like,
      time: sr.ctime ? new Date(sr.ctime * 1000).toISOString() : null
    }))
  });

  const comments = (d.data?.replies || []).map(formatReply);

  // Include top/pinned comments on first page
  let top = null;
  if (pn === 1 && d.data?.top_replies?.length) {
    top = d.data.top_replies.map(formatReply);
  }

  return {
    bvid,
    aid,
    title: viewData.data?.title,
    page: pn,
    total: d.data?.page?.count || 0,
    count: comments.length,
    sort: sort === 0 ? 'by_time' : 'by_likes',
    top_comments: top,
    comments
  };
}
