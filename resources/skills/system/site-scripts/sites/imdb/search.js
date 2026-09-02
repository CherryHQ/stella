/* @meta
{
  "name": "imdb/search",
  "description": "IMDb 电影搜索",
  "domain": "www.imdb.com",
  "args": {
    "query": {
      "type": "string",
      "required": true,
      "description": "搜索关键词"
    }
  },
  "readOnly": true,
  "example": "bb-browser site imdb/search --query inception"
}
*/

async function(args) {
  const query = (args.query || '').trim().toLowerCase();
  if (!query) return {error: 'query is required'};
  const firstChar = query[0];
  const url = `https://v3.sg.media-imdb.com/suggestion/${firstChar}/${encodeURIComponent(query)}.json`;
  const resp = await fetch(url);
  if (!resp.ok) return {error: 'HTTP ' + resp.status};
  const data = await resp.json();
  const items = (data.d || []).map(item => ({
    title: item.l,
    year: item.y,
    type: item.q,
    stars: item.s,
    imdbId: item.id,
    imageUrl: item.i?.imageUrl,
    url: item.id ? `https://www.imdb.com/title/${item.id}/` : undefined
  }));
  return {count: items.length, results: items};
}
