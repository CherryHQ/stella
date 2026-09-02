/* @meta
{
  "description": "Search X / Twitter posts via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "q": {
      "required": true,
      "description": "Search query"
    },
    "feed": {
      "required": false,
      "description": "Search tab: latest, top, or media"
    },
    "count": {
      "required": false,
      "description": "Page size"
    },
    "cursor": {
      "required": false,
      "description": "Pagination cursor"
    },
    "lang": {
      "required": false,
      "description": "Target language for inline translations, e.g. en, es, zh-cn"
    }
  },
  "readOnly": true
}
*/

async function(args) {
  if (!args.q) return {error: 'Missing argument: q', hint: 'Provide a search query'};

  const params = ['q=' + encodeURIComponent(args.q)];
  if (args.feed) params.push('feed=' + encodeURIComponent(args.feed));
  if (args.count) params.push('count=' + encodeURIComponent(args.count));
  if (args.cursor) params.push('cursor=' + encodeURIComponent(args.cursor));
  if (args.lang) params.push('lang=' + encodeURIComponent(args.lang));

  const url = 'https://api.fxtwitter.com/2/search?' + params.join('&');
  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
