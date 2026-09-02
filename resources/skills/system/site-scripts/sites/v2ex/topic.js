/* @meta
{
  "name": "v2ex/topic",
  "description": "获取 V2EX 主题详情和回复",
  "domain": "www.v2ex.com",
  "args": {
    "id": {"required": true, "description": "Topic ID"}
  },
  "readOnly": true,
  "example": "bb-browser site v2ex/topic 1024"
}
*/

async function(args) {
  if (!args.id) return {error: 'Missing argument: id', hint: 'Provide a topic ID'};
  const [topicResp, repliesResp] = await Promise.all([
    fetch('https://www.v2ex.com/api/topics/show.json?id=' + args.id),
    fetch('https://www.v2ex.com/api/replies/show.json?topic_id=' + args.id)
  ]);
  if (!topicResp.ok) return {error: 'HTTP ' + topicResp.status};
  const topics = await topicResp.json();
  const replies = repliesResp.ok ? await repliesResp.json() : [];
  const t = topics[0];
  if (!t) return {error: 'Topic not found'};
  return {
    id: t.id, title: t.title, content: t.content,
    node: t.node?.title, author: t.member?.username,
    replies: t.replies, created: t.created, url: t.url,
    comments: replies.map(r => ({
      author: r.member?.username, content: r.content, created: r.created
    }))
  };
}
