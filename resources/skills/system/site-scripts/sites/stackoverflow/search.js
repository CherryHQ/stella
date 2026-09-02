/* @meta
{
  "name": "stackoverflow/search",
  "description": "Search Stack Overflow questions",
  "domain": "stackoverflow.com",
  "args": {
    "query": {"required": true, "description": "Search query"},
    "count": {"required": false, "description": "Number of results (default 10, max 50)"}
  },
  "readOnly": true,
  "example": "bb-browser site stackoverflow/search \"python async await\""
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query', hint: 'Provide a search query string'};
  const count = Math.min(parseInt(args.count) || 10, 50);
  const url = 'https://api.stackexchange.com/2.3/search?order=desc&sort=relevance&intitle='
    + encodeURIComponent(args.query)
    + '&site=stackoverflow&pagesize=' + count;
  const resp = await fetch(url);
  if (!resp.ok) return {error: 'HTTP ' + resp.status};
  const data = await resp.json();
  if (data.error_id) return {error: data.error_name, message: data.error_message};
  const items = data.items || [];
  return {
    query: args.query,
    count: items.length,
    has_more: data.has_more,
    quota_remaining: data.quota_remaining,
    questions: items.map(q => ({
      id: q.question_id,
      title: q.title,
      url: q.link,
      score: q.score,
      answers: q.answer_count,
      views: q.view_count,
      tags: q.tags,
      author: q.owner?.display_name,
      is_answered: q.is_answered,
      created: q.creation_date,
      last_activity: q.last_activity_date
    }))
  };
}
