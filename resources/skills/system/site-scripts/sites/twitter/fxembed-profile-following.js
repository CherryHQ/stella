/* @meta
{
  "description": "List X / Twitter accounts a user follows via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "handle": {
      "required": true,
      "description": "Username without @, or numeric user id as id:123"
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
  if (!args.handle) return {error: 'Missing argument: handle', hint: 'Provide username without @, or numeric user id as id:123'};

  const handle = String(args.handle).replace(/^@/, '');
  const params = [];
  if (args.count) params.push('count=' + encodeURIComponent(args.count));
  if (args.cursor) params.push('cursor=' + encodeURIComponent(args.cursor));

  let url = `https://api.fxtwitter.com/2/profile/${encodeURIComponent(handle)}/following`;
  if (params.length) url += '?' + params.join('&');

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
