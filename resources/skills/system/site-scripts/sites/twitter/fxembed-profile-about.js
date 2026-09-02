/* @meta
{
  "description": "Fetch X / Twitter About Account stats via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "handle": {
      "required": true,
      "description": "Username without @, or numeric user id as id:123"
    }
  },
  "readOnly": true
}
*/

async function(args) {
  if (!args.handle) return {error: 'Missing argument: handle', hint: 'Provide username without @, or numeric user id as id:123'};

  const handle = String(args.handle).replace(/^@/, '');
  const params = [];

  let url = `https://api.fxtwitter.com/2/profile/${encodeURIComponent(handle)}/about`;
  if (params.length) url += '?' + params.join('&');

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
