/* @meta
{
  "description": "Fetch X / Twitter trending topics via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "type": {
      "required": false,
      "description": "Explore timeline kind: trending"
    },
    "count": {
      "required": false,
      "description": "Number of trends, max 50"
    }
  },
  "readOnly": true
}
*/

async function(args) {
  const params = [];
  if (args.type) params.push('type=' + encodeURIComponent(args.type));
  if (args.count) params.push('count=' + encodeURIComponent(args.count));

  let url = 'https://api.fxtwitter.com/2/trends';
  if (params.length) url += '?' + params.join('&');

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
