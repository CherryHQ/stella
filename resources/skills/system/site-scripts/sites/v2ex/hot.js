/* @meta
{
  "name": "v2ex/hot",
  "description": "获取 V2EX 最热主题",
  "domain": "www.v2ex.com",
  "args": {},
  "readOnly": true,
  "example": "bb-browser site v2ex/hot"
}
*/

async function(args) {
  const resp = await fetch('https://www.v2ex.com/api/topics/hot.json');
  if (!resp.ok) return {error: 'HTTP ' + resp.status};
  const topics = await resp.json();
  return {count: topics.length, topics: topics.map(t => ({
    id: t.id, title: t.title, content: (t.content || '').substring(0, 300),
    node: t.node?.title, nodeSlug: t.node?.name,
    author: t.member?.username, replies: t.replies,
    created: t.created, url: t.url
  }))};
}
