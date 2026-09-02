/* @meta
{
  "name": "douban/search",
  "description": "Search Douban across movies, books, and music",
  "domain": "www.douban.com",
  "args": {
    "keyword": {"required": true, "description": "Search keyword (Chinese or English)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site douban/search 三体"
}
*/

async function(args) {
  if (!args.keyword) return {error: 'Missing argument: keyword'};
  const q = encodeURIComponent(args.keyword);

  // Try the rich search_suggest endpoint (requires www.douban.com origin)
  var resp;
  var usedFallback = false;
  try {
    resp = await fetch('https://www.douban.com/j/search_suggest?q=' + q, {credentials: 'include'});
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
  } catch (e) {
    // Fallback: use movie.douban.com subject_suggest (works cross-subdomain via same eTLD+1 cookies)
    try {
      resp = await fetch('/j/subject_suggest?q=' + q, {credentials: 'include'});
      usedFallback = true;
    } catch (e2) {
      return {error: 'Search failed: ' + e2.message, hint: 'Not logged in? Navigate to www.douban.com first.'};
    }
  }

  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  var d = await resp.json();

  if (usedFallback) {
    // subject_suggest returns an array directly
    var items = (Array.isArray(d) ? d : []).map(function(c, i) {
      return {
        rank: i + 1,
        id: c.id,
        type: c.type === 'movie' ? 'movie' : c.type === 'b' ? 'book' : c.type || 'unknown',
        title: c.title,
        subtitle: c.sub_title || '',
        rating: null,
        info: '',
        year: c.year || null,
        cover: c.img || c.pic || null,
        url: c.url
      };
    });
    return {
      keyword: args.keyword,
      count: items.length,
      results: items,
      suggestions: [],
      note: 'Limited results (movie/book only). For richer results, navigate to www.douban.com first.'
    };
  }

  // Rich search_suggest response with cards
  var cards = (d.cards || []).map(function(c, i) {
    var id = c.url && c.url.match(/subject\/(\d+)/);
    id = id ? id[1] : null;
    var ratingMatch = c.card_subtitle && c.card_subtitle.match(/([\d.]+)分/);
    return {
      rank: i + 1,
      id: id,
      type: c.type || 'unknown',
      title: c.title,
      subtitle: c.abstract || '',
      rating: ratingMatch ? parseFloat(ratingMatch[1]) : null,
      info: c.card_subtitle || '',
      year: c.year || null,
      cover: c.cover_url || null,
      url: c.url
    };
  });

  var suggestions = d.words || [];

  return {
    keyword: args.keyword,
    count: cards.length,
    results: cards,
    suggestions: suggestions
  };
}
