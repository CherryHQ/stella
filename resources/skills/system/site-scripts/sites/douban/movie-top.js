/* @meta
{
  "name": "douban/movie-top",
  "description": "Get top rated movies by genre from Douban charts",
  "domain": "movie.douban.com",
  "args": {
    "genre": {"required": false, "description": "Genre (default: 剧情). Options: 剧情/喜剧/动作/爱情/科幻/动画/悬疑/惊悚/恐怖/纪录片/短片/情色/音乐/歌舞/家庭/儿童/传记/历史/战争/犯罪/西部/奇幻/冒险/灾难/武侠/古装/运动/黑色电影"},
    "count": {"required": false, "description": "Number of results (default: 20, max: 50)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site douban/movie-top 科幻 10"
}
*/

async function(args) {
  // Genre name to type ID mapping
  const genreMap = {
    '剧情': 11, '喜剧': 24, '动作': 5, '爱情': 13, '科幻': 17,
    '动画': 25, '悬疑': 10, '惊悚': 19, '恐怖': 20, '纪录片': 1,
    '短片': 23, '情色': 6, '音乐': 14, '歌舞': 7, '家庭': 28,
    '儿童': 8, '传记': 2, '历史': 4, '战争': 22, '犯罪': 3,
    '西部': 27, '奇幻': 26, '冒险': 15, '灾难': 12, '武侠': 29,
    '古装': 30, '运动': 18, '黑色电影': 31
  };

  const genre = args.genre || '剧情';
  const typeId = genreMap[genre];
  if (!typeId) return {error: 'Unknown genre: ' + genre, hint: 'Available: ' + Object.keys(genreMap).join(', ')};

  const count = Math.min(parseInt(args.count) || 20, 50);

  const url = 'https://movie.douban.com/j/chart/top_list?type=' + typeId
    + '&interval_id=100%3A90&action=&start=0&limit=' + count;

  const resp = await fetch(url, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Not logged in?'};
  const data = await resp.json();

  if (!Array.isArray(data)) return {error: 'Unexpected response format'};

  const items = data.map(function(s) {
    return {
      rank: s.rank,
      id: s.id,
      title: s.title,
      rating: parseFloat(s.score) || null,
      votes: s.vote_count,
      types: s.types,
      regions: s.regions,
      actors: (s.actors || []).slice(0, 5),
      release_date: s.release_date,
      cover: s.cover_url,
      url: s.url,
      playable: s.is_playable
    };
  });

  return {
    genre: genre,
    count: items.length,
    available_genres: Object.keys(genreMap),
    items: items
  };
}
