/* @meta
{
  "name": "douban/movie-hot",
  "description": "Get hot/trending movies or TV shows on Douban by tag",
  "domain": "movie.douban.com",
  "args": {
    "type": {"required": false, "description": "Type: movie (default) or tv"},
    "tag": {"required": false, "description": "Tag filter (default: 热门). Movies: 热门/最新/豆瓣高分/冷门佳片/华语/欧美/韩国/日本. TV: 热门/国产剧/综艺/美剧/日剧/韩剧/日本动画/纪录片"},
    "count": {"required": false, "description": "Number of results (default: 20, max: 50)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site douban/movie-hot movie 豆瓣高分"
}
*/

async function(args) {
  const type = (args.type || 'movie').toLowerCase();
  if (type !== 'movie' && type !== 'tv') return {error: 'Invalid type. Use "movie" or "tv"'};

  const tag = args.tag || '热门';
  const count = Math.min(parseInt(args.count) || 20, 50);

  const url = 'https://movie.douban.com/j/search_subjects?type=' + type
    + '&tag=' + encodeURIComponent(tag)
    + '&page_limit=' + count
    + '&page_start=0';

  const resp = await fetch(url, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const d = await resp.json();

  if (!d.subjects) return {error: 'No data returned', hint: 'Invalid tag or not logged in?'};

  const items = d.subjects.map(function(s, i) {
    return {
      rank: i + 1,
      id: s.id,
      title: s.title,
      rating: s.rate ? parseFloat(s.rate) : null,
      cover: s.cover,
      url: s.url,
      playable: s.playable,
      is_new: s.is_new,
      episodes_info: s.episodes_info || null
    };
  });

  // Also fetch available tags for reference
  var tagsResp = await fetch('https://movie.douban.com/j/search_tags?type=' + type + '&source=index', {credentials: 'include'});
  var availableTags = [];
  if (tagsResp.ok) {
    var tagsData = await tagsResp.json();
    availableTags = tagsData.tags || [];
  }

  return {
    type: type,
    tag: tag,
    count: items.length,
    available_tags: availableTags,
    items: items
  };
}
