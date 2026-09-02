/* @meta
{
  "name": "reddit/hot",
  "description": "获取 subreddit 热门帖子（或 Reddit 首页）",
  "domain": "www.reddit.com",
  "args": {
    "subreddit": {"required": false, "description": "Subreddit name without r/ prefix (omit for front page)"},
    "count": {"required": false, "description": "Number of posts (default 25, max 100)"},
    "after": {"required": false, "description": "Fullname of the last item for pagination (e.g. t3_abc123)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site reddit/hot ClaudeAI"
}
*/

async function(args) {
  const count = Math.min(parseInt(args.count) || 25, 100);
  const base = args.subreddit ? '/r/' + args.subreddit : '';
  let url = base + '/hot.json?limit=' + count + '&raw_json=1';
  if (args.after) url += '&after=' + args.after;

  const resp = await fetch(url, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: resp.status === 404 ? 'Subreddit not found' : 'API error'};
  const d = await resp.json();
  if (!d.data?.children) return {error: 'Unexpected response', hint: 'Reddit may be rate-limiting or returning a login page'};

  const posts = d.data.children.map((c, i) => ({
    rank: i + 1,
    id: c.data.name,
    title: c.data.title,
    author: c.data.author,
    subreddit: c.data.subreddit_name_prefixed,
    score: c.data.score,
    upvote_ratio: c.data.upvote_ratio,
    num_comments: c.data.num_comments,
    created_utc: c.data.created_utc,
    url: c.data.url,
    permalink: 'https://www.reddit.com' + c.data.permalink,
    selftext_preview: (c.data.selftext || '').substring(0, 200),
    is_self: c.data.is_self,
    link_flair_text: c.data.link_flair_text || null
  }));

  return {
    subreddit: args.subreddit || 'front page',
    count: posts.length,
    after: d.data.after || null,
    posts
  };
}
