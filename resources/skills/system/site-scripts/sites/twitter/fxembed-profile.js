/* @meta
{
  "description": "Fetch an X / Twitter user profile via FxEmbed",
  "domain": "api.fxtwitter.com",
  "args": {
    "handle": {
      "required": true,
      "description": "Username without @, or numeric user id as id:123"
    },
    "about_account": {
      "required": false,
      "description": "Truthy value to include account metadata when available"
    }
  },
  "readOnly": true
}
*/

async function(args) {
  if (!args.handle) return {error: 'Missing argument: handle', hint: 'Provide a username without @, or id:123'};

  const handle = String(args.handle).replace(/^@/, '');
  let url = `https://api.fxtwitter.com/2/profile/${encodeURIComponent(handle)}`;
  if (args.about_account) url += '?about_account=' + encodeURIComponent(args.about_account);

  const resp = await fetch(url, {
    headers: {'accept': 'application/json'}
  });

  if (!resp.ok) return {error: 'HTTP ' + resp.status};

  return await resp.json();
}
