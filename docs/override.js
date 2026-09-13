// pgb docs — per-page hook.
// The landing page (/) has its own hero, so the theme's page-header
// (frontmatter title + description) is hidden there. A MutationObserver
// re-applies the class on client-side route changes.
const apply = () => {
  const home = window.location.pathname === '/';
  document.documentElement.classList.toggle('pgb-home', home);
};

apply();
new MutationObserver(apply).observe(document.body, { childList: true, subtree: true });
