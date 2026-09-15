(function () {
  "use strict";

  const path = window.location.pathname;
  const match = path.match(/^(.*?\/)(main|v\d+\.\d+[^\/]*)(\/|$)/);
  if (!match) return;

  const root = match[1];
  const currentVersion = match[2];

  fetch(root + "versions.json")
    .then((response) => response.json())
    .then((versions) => {
      const latest =
        versions.find((v) => (v.aliases || []).includes("latest")) || versions[0];
      if (!latest) return;

      let message;
      if (currentVersion === "main") {
        message = "You are viewing the docs for an unreleased version.";
      } else if (currentVersion !== latest.version) {
        message = "You are viewing the docs for an old version.";
      }
      if (!message) return;

      const rest = path.slice(root.length + currentVersion.length);
      createBanner(message, root + latest.version + rest);
    })
    .catch(() => {});

  function createBanner(message, latestPath) {
    const banner = document.createElement("div");
    banner.id = "version-banner";
    banner.innerHTML = `
    <strong>${message}</strong>
    <a href="${latestPath}" id="latest-version-link">Click here to go to the latest stable version.</a>
  `;
    document.body.insertBefore(banner, document.body.firstChild);
  }
})();
