/* @meta
{
  "name": "douban/movie",
  "description": "Get detailed movie/TV info with rating, cast, and hot reviews from Douban",
  "domain": "movie.douban.com",
  "args": {
    "id": {"required": true, "description": "Douban subject ID (e.g. 1292052 for The Shawshank Redemption)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site douban/movie 1292052"
}
*/

async function(args) {
  if (!args.id) return {error: 'Missing argument: id'};
  const id = String(args.id).trim();

  // Fetch structured data from the JSON API
  const apiResp = await fetch('https://movie.douban.com/j/subject_abstract?subject_id=' + id, {credentials: 'include'});
  if (!apiResp.ok) return {error: 'HTTP ' + apiResp.status, hint: 'Not logged in or invalid ID?'};
  const apiData = await apiResp.json();
  if (apiData.r !== 0 || !apiData.subject) return {error: 'Subject not found', hint: 'Check the ID'};

  const s = apiData.subject;

  // Also fetch the HTML page for richer data (summary, rating distribution, hot comments)
  const pageResp = await fetch('https://movie.douban.com/subject/' + id + '/', {credentials: 'include'});
  let summary = '', ratingDist = {}, hotComments = [], recommendations = [], votes = null, info = '';

  if (pageResp.ok) {
    const html = await pageResp.text();
    const doc = new DOMParser().parseFromString(html, 'text/html');

    // Summary
    const summaryEl = doc.querySelector('[property="v:summary"]');
    summary = summaryEl ? summaryEl.textContent.trim() : '';

    // Vote count
    const votesEl = doc.querySelector('[property="v:votes"]');
    votes = votesEl ? parseInt(votesEl.textContent) : null;

    // Info block
    const infoEl = doc.querySelector('#info');
    info = infoEl ? infoEl.innerText || infoEl.textContent.trim() : '';

    // Rating distribution
    doc.querySelectorAll('.ratings-on-weight .item').forEach(function(el) {
      var star = el.querySelector('span:first-child');
      var pct = el.querySelector('.rating_per');
      if (star && pct) ratingDist[star.textContent.trim()] = pct.textContent.trim();
    });

    // Hot comments
    doc.querySelectorAll('#hot-comments .comment-item').forEach(function(el) {
      var author = el.querySelector('.comment-info a');
      var rating = el.querySelector('.comment-info .rating');
      var content = el.querySelector('.short');
      var voteCount = el.querySelector('.vote-count');
      var date = el.querySelector('.comment-time');
      hotComments.push({
        author: author ? author.textContent.trim() : '',
        rating: rating ? rating.title : '',
        content: content ? content.textContent.trim() : '',
        votes: voteCount ? parseInt(voteCount.textContent) || 0 : 0,
        date: date ? date.textContent.trim() : ''
      });
    });

    // Recommendations
    doc.querySelectorAll('.recommendations-bd dl').forEach(function(dl) {
      var a = dl.querySelector('dd a');
      if (a) {
        var recId = a.href?.match(/subject\/(\d+)/)?.[1];
        recommendations.push({title: a.textContent.trim(), id: recId, url: a.href});
      }
    });
  }

  // Parse info block for structured fields
  const parseInfo = function(text) {
    const result = {};
    const lines = text.split('\n').map(function(l) { return l.trim(); }).filter(Boolean);
    lines.forEach(function(line) {
      var m = line.match(/^(.+?):\s*(.+)$/);
      if (m) result[m[1].trim()] = m[2].trim();
    });
    return result;
  };
  const infoFields = parseInfo(info);

  return {
    id: s.id,
    title: s.title,
    subtype: s.subtype,
    is_tv: s.is_tv,
    rating: parseFloat(s.rate) || null,
    votes: votes,
    rating_distribution: ratingDist,
    directors: s.directors,
    actors: s.actors,
    types: s.types,
    region: s.region,
    duration: s.duration,
    release_year: s.release_year,
    episodes_count: s.episodes_count || null,
    imdb: infoFields['IMDb'] || null,
    alias: infoFields['又名'] || null,
    language: infoFields['语言'] || null,
    release_date: infoFields['上映日期'] || infoFields['首播'] || null,
    summary: summary,
    playable: s.playable,
    url: s.url,
    hot_comments: hotComments,
    recommendations: recommendations
  };
}
