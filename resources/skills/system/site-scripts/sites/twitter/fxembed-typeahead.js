/* @meta
{
  "description": "Search X / Twitter typeahead suggestions via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "q": {
      "required": true,
      "description": "Prefix or query string"
    },
    "result_type": {
      "required": false,
      "description": "Comma-separated suggestion kinds: events, users, topics"
    },
    "src": {
      "required": false,
      "description": "Upstream src hint, default search_box"
    }
  },
  "readOnly": true
}
*/

async function(args) {
  if (!args.q) return {error: 'Missing argument: q', hint: 'Provide prefix or query string'};

  const params = ['q=' + encodeURIComponent(args.q)];
  if (args.result_type) params.push('result_type=' + encodeURIComponent(args.result_type));
  if (args.src) params.push('src=' + encodeURIComponent(args.src));

  let url = 'https://api.fxtwitter.com/2/typeahead';
  if (params.length) url += '?' + params.join('&');

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
