(function () {
  "use strict";

  const UNRELEASED_VERSION = "main";

  function getCurrentVersion() {
    const path = window.location.pathname;
    const versionMatch = path.match(/\/(main|v\d+\.\d+[^\/]*)/);
    return versionMatch ? versionMatch[1] : null;
  }

  function getAvailableVersions() {
    const versionList = document.querySelector("ul.md-version__list");
    if (!versionList) return [];

    const links = Array.from(
      versionList.querySelectorAll("a.md-version__link")
    );

    return links
      .map((link) => {
        const href = link.href || link.getAttribute("href");
        const match = href.match(/\/(v\d+\.\d+[^\/]*)\//);
        return match ? match[1] : null;
      })
      .filter((v) => v && /^v\d+\.\d+/.test(v))
      .filter((v, i, arr) => arr.indexOf(v) === i);
  }

  function getLatestVersionPath() {
    const versions = getAvailableVersions();
    const latestVersion = versions[0];

    if (!latestVersion) {
      return null;
    }

    const currentPath = window.location.pathname;

    return (
      currentPath.replace(/\/main(\/|$)/, `/${latestVersion}$1`) ||
      `/${latestVersion}/`
    );
  }

  function createBanner() {
    const banner = document.createElement("div");
    banner.id = "version-banner";
    banner.innerHTML = `
    <strong>You are viewing the docs for an unreleased version.</strong>
    <a href="#" id="latest-version-link">Click here to go to the latest stable version.</a>
  `;

    banner.querySelector("#latest-version-link").addEventListener("click", function (e) {
      const path = getLatestVersionPath();
      if (path) {
        e.preventDefault();
        window.location.href = path;
      }
    });

    document.body.insertBefore(banner, document.body.firstChild);

    return banner;
  }

  if (getCurrentVersion() !== UNRELEASED_VERSION) {
    return;
  }

  createBanner();
})();
