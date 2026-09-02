/* @meta
{
  "name": "douban/top250",
  "description": "Get Douban Top 250 movies list",
  "domain": "movie.douban.com",
  "args": {
    "start": {"required": false, "description": "Start position (default: 0, step by 25). Use 0 for #1-25, 25 for #26-50, etc."}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site douban/top250"
}
*/

async function(args) {
  const start = parseInt(args.start) || 0;

  const resp = await fetch('https://movie.douban.com/top250?start=' + start, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const html = await resp.text();
  const doc = new DOMParser().parseFromString(html, 'text/html');

  const items = [];
  doc.querySelectorAll('.grid_view .item').forEach(function(el) {
    var rank = el.querySelector('.pic em');
    var titleEl = el.querySelector('.hd a .title');
    var otherTitleEl = el.querySelector('.hd a .other');
    var ratingEl = el.querySelector('.rating_num');
    var link = el.querySelector('.hd a');
    var quoteEl = el.querySelector('.quote .inq') || el.querySelector('.quote span');
    var infoEl = el.querySelector('.bd p');

    // Vote count is in a span like "3268455人评价"
    var voteSpans = el.querySelectorAll('.bd div span');
    var votes = null;
    voteSpans.forEach(function(sp) {
      var m = sp.textContent.match(/(\d+)人评价/);
      if (m) votes = parseInt(m[1]);
    });

    var id = link?.href?.match(/subject\/(\d+)/)?.[1];

    // Parse info line for director, year, region, genre
    var infoText = infoEl ? infoEl.textContent.trim() : '';
    var lines = infoText.split('\n').map(function(l) { return l.trim(); }).filter(Boolean);
    var directorLine = lines[0] || '';
    var metaLine = lines[1] || '';
    var metaParts = metaLine.split('/').map(function(p) { return p.trim(); });

    items.push({
      rank: rank ? parseInt(rank.textContent) : null,
      id: id,
      title: titleEl ? titleEl.textContent.trim() : '',
      other_title: otherTitleEl ? otherTitleEl.textContent.trim().replace(/^\s*\/\s*/, '') : '',
      rating: ratingEl ? parseFloat(ratingEl.textContent) : null,
      votes: votes,
      quote: quoteEl ? quoteEl.textContent.trim() : '',
      year: metaParts[0] || '',
      region: metaParts[1] || '',
      genre: metaParts[2] || '',
      url: link ? link.href : ''
    });
  });

  return {
    start: start,
    count: items.length,
    total: 250,
    has_more: start + items.length < 250,
    next_start: start + items.length < 250 ? start + 25 : null,
    items: items
  };
}
