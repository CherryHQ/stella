/* @meta
{
  "description": "Fetch an unrolled X / Twitter thread and replies by ID via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "id": {
      "required": true,
      "description": "Tweet / post ID"
    },
    "ranking_mode": {
      "required": false,
      "description": "Reply ranking mode: likes or recency"
    },
    "cursor": {
      "required": false,
      "description": "Pagination cursor"
    },
    "about_account": {
      "required": false,
      "description": "Truthy value to include account metadata when available"
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
  if (!args.id) return {error: 'Missing argument: id', hint: 'Provide tweet / post ID'};

  const params = [];
  if (args.ranking_mode) params.push('ranking_mode=' + encodeURIComponent(args.ranking_mode));
  if (args.cursor) params.push('cursor=' + encodeURIComponent(args.cursor));
  if (args.about_account) params.push('about_account=' + encodeURIComponent(args.about_account));
  if (args.lang) params.push('lang=' + encodeURIComponent(args.lang));

  let url = `https://api.fxtwitter.com/2/conversation/${encodeURIComponent(args.id)}`;
  if (params.length) url += '?' + params.join('&');

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
