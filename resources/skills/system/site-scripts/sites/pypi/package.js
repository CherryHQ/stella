/* @meta
{
  "name": "pypi/package",
  "description": "获取 Python 包详情",
  "domain": "pypi.org",
  "args": {
    "name": "包名"
  },
  "readOnly": true,
  "example": "bb-browser site pypi/package \"requests\""
}
*/

async function(args) {
  const name = args.name || args._text;
  if (!name) return {error: 'Missing package name. Usage: bb-browser site pypi/package "PACKAGE_NAME"'};
  const url = `https://pypi.org/pypi/${encodeURIComponent(name)}/json`;
  const resp = await fetch(url);
  if (!resp.ok) return {error: 'HTTP ' + resp.status};
  const data = await resp.json();
  const info = data.info || {};
  return {
    name: info.name,
    version: info.version,
    summary: info.summary,
    author: info.author,
    author_email: info.author_email,
    license: info.license,
    home_page: info.home_page,
    project_url: info.project_url,
    package_url: info.package_url,
    requires_python: info.requires_python,
    keywords: info.keywords,
    classifiers: info.classifiers,
    project_urls: info.project_urls,
    requires_dist: info.requires_dist
  };
}
