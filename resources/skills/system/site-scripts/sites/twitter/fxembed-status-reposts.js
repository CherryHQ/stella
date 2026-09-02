/* @meta
{
  "description": "List reposters of an X / Twitter post via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "id": {
      "required": true,
      "description": "Tweet / post ID"
    },
    "count": {
      "required": false,
      "description": "Page size, 1-100"
    },
    "cursor": {
      "required": false,
      "description": "Pagination cursor"
    }
  },
  "readOnly": true
}
*/

async function(args) {
  if (!args.id) return {error: 'Missing argument: id', hint: 'Provide tweet / post ID'};

  const params = [];
  if (args.count) params.push('count=' + encodeURIComponent(args.count));
  if (args.cursor) params.push('cursor=' + encodeURIComponent(args.cursor));

  let url = `https://api.fxtwitter.com/2/status/${encodeURIComponent(args.id)}/reposts`;
  if (params.length) url += '?' + params.join('&');

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
