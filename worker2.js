/**
 * Cloudflare Worker: Path-Based Reverse Proxy with Service Worker Injection
 *
 * This worker implements a reverse proxy using a path-based routing scheme.
 * It uses a clever reversed-domain path to handle cross-subdomain cookies correctly.
 *
 * HOW IT WORKS:
 * 1.  A request to '/' serves a dynamic HTML page with a URL input and a client-side
 * bookmarking system that uses localStorage.
 * 2.  A request to '/sw.js' serves the service worker javascript, versioned for updates.
 * 3.  The HTML page registers the versioned '/sw.js' service worker. This worker intercepts
 * all subsequent navigation and fetch requests from the browser tab.
 * 4.  The Service Worker rewrites the request URL. For example, a request to
 * `https://google.com` is rewritten to `/proxy/com.google./` on the worker's domain.
 * It also intelligently handles relative path requests from already-proxied pages.
 * 5.  The Cloudflare worker receives the request at the `/proxy/...` endpoint.
 * 6.  It parses the reversed domain, un-reverses it to find the target host,
 * and forwards the request.
 * 7.  On response, it rewrites `Set-Cookie` and `Location` headers. It also sets a
 * restrictive CSP header and removes other problematic tags like <meta refresh>.
 * 8.  For HTML responses, it uses HTMLRewriter to inject a client-side script that
 * intercepts clicks on links and forms, and adds a robust "Back to Home" FAB.
 * 9.  `Set-Cookie` `Domain` attributes are converted to `Path` attributes
 * using the same reversed-domain logic, preserving cross-subdomain behavior.
 * 10. `Expires` and `Max-Age` are removed from cookies to make them session-only.
 */

// ===================================================================================
// Configuration & Main Worker Logic
// ===================================================================================

const SW_VERSION = '1.0.31'; // Increment to force service worker updates

/**
 * A handler class for HTMLRewriter to inject a script at the end of an element.
 */
class ScriptInjector {
  constructor(script) {
    this.script = script;
  }

  element(element) {
    // Append the script to the end of the element.
    element.append(this.script, { html: true });
  }
}

/**
 * A handler class for HTMLRewriter to remove specific, problematic elements.
 */
class ElementRemover {
  element(element) {
    // Check for <meta http-equiv="refresh">
    if (element.tagName === 'meta' && element.getAttribute('http-equiv')?.toLowerCase() === 'refresh') {
      element.remove();
    }
    // Remove <base> tags to prevent relative URL conflicts.
    if (element.tagName === 'base') {
      element.remove();
    }
  }
}


export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const path = url.pathname;

    if (path.startsWith("/proxy/")) {
      return this.handleProxy(request);
    }

    if (path === "/sw.js") {
      return this.serveServiceWorker();
    }

    if (path === "/") {
      return this.serveIndexPage();
    }
    
    return new Response("Not Found", { status: 404 });
  },

  /**
   * Handles the actual proxying of requests.
   * @param {Request} request The incoming request to the /proxy/ endpoint.
   */
  async handleProxy(request) {
    const url = new URL(request.url);

    // 1. Extract target host and path from the request URL.
    const pathSegments = url.pathname.substring("/proxy/".length).split('/');
    const reversedHost = pathSegments.shift(); 
    const targetPath = "/" + pathSegments.join('/');

    const targetHost = reversedHost.split('.').reverse().join('.');
    const targetUrl = new URL(targetPath + url.search, `https://${targetHost}`);

    // 2. Forward the request to the target origin.
    const newRequestHeaders = new Headers(request.headers);
    newRequestHeaders.set('Host', targetHost);
    newRequestHeaders.set('Referer', `https://${targetHost}/`);

    const originResponse = await fetch(targetUrl.toString(), {
      method: request.method,
      headers: newRequestHeaders,
      body: request.body,
      redirect: 'manual',
    });

    // 3. Process and rewrite headers on the response.
    const responseHeaders = new Headers(originResponse.headers);

    // Remove the origin's CSP and set our own restrictive one.
    // This prevents the proxied page from creating iframes or registering other workers.
    responseHeaders.set('Content-Security-Policy', "frame-src 'none'; worker-src 'self';");

    // Rewrite Set-Cookie headers
    const setCookieHeaders = responseHeaders.getAll('Set-Cookie');
    if (setCookieHeaders.length > 0) {
      responseHeaders.delete('Set-Cookie');
      setCookieHeaders.forEach(cookieHeader => {
        const rewrittenCookie = this.rewriteCookie(cookieHeader, targetHost);
        responseHeaders.append('Set-Cookie', rewrittenCookie);
      });
    }

    // Rewrite Location header for redirects
    const location = responseHeaders.get('Location');
    if (location) {
      const locationUrl = new URL(location, `https://${targetHost}`);
      const reversedLocationHost = locationUrl.hostname.split('.').reverse().join('.');
      const newLocation = `/proxy/${reversedLocationHost}${locationUrl.pathname}${locationUrl.search}`;
      responseHeaders.set('Location', newLocation);
    }
    
    // Remove other headers that could break the proxy.
    responseHeaders.delete('Strict-Transport-Security');
    
    // 4. Check if the response is HTML.
    const contentType = responseHeaders.get('content-type') || '';
    if (contentType.includes('text/html')) {
        // Create a new response with the MODIFIED headers and the original body.
        const modifiableResponse = new Response(originResponse.body, {
            status: originResponse.status,
            statusText: originResponse.statusText,
            headers: responseHeaders // Use the modified headers
        });

        const scriptInjector = new ScriptInjector(this.getInjectionScript());
        const elementRemover = new ElementRemover();
        
        // Return a transformed response using HTMLRewriter.
        return new HTMLRewriter()
            .on('head', scriptInjector)
            .on('meta[http-equiv="refresh"]', elementRemover)
            .on('base', elementRemover)
            .transform(modifiableResponse);
    }

    // 5. For non-HTML content, return the response with our modified headers.
    return new Response(originResponse.body, {
      status: originResponse.status,
      statusText: originResponse.statusText,
      headers: responseHeaders,
    });
  },

  /**
   * Returns the client-side JavaScript to be injected into HTML pages.
   * This script rewrites links and adds a "Back to Home" FAB.
   */
  getInjectionScript() {
    return `
<script>
    (function() {
        // Since this script is in the <head>, we must wait for the DOM to be ready
        // before we can interact with it.
        function onDomReady() {
            const isProxied = window.location.pathname.startsWith('/proxy/');
            if (!isProxied) return;

            function getProxiedUrl(targetUrl) {
                if (targetUrl.hostname === window.location.hostname && targetUrl.pathname.startsWith('/proxy/')) {
                    return null; // Already proxied
                }

                if (targetUrl.hostname !== window.location.hostname) {
                    const reversedHost = targetUrl.hostname.split('.').reverse().join('.');
                    return \`/proxy/\${reversedHost}\${targetUrl.pathname}\${targetUrl.search}\${targetUrl.hash}\`;
                } else {
                    const currentPathParts = window.location.pathname.substring('/proxy/'.length).split('/');
                    const currentReversedHost = currentPathParts[0];
                    return \`/proxy/\${currentReversedHost}\${targetUrl.pathname}\${targetUrl.search}\${targetUrl.hash}\`;
                }
            }

            document.addEventListener('click', function(e) {
                const link = e.target.closest('a');
                
                if (link) {
                    // The FAB is a div, not a link, so it's not caught here.
                    if (link.protocol === 'javascript:' || link.getAttribute('href')?.startsWith('#')) {
                        return;
                    }
                    
                    e.preventDefault();
                    // Resolve the href to an absolute URL
                    const targetUrl = new URL(link.getAttribute('href'), window.location.href);
                    const proxiedUrl = getProxiedUrl(targetUrl);
                    
                    if (proxiedUrl) {
                        window.location.href = proxiedUrl;
                    } else {
                        window.location.href = targetUrl.href; // Navigate to already proxied URL
                    }
                    return;
                }

                const submitButton = e.target.closest('input[type="submit"], button[type="submit"]');
                if (submitButton) {
                    const form = submitButton.form;
                    if (form) {
                        e.preventDefault();
                        const formActionUrl = new URL(form.getAttribute('action') || window.location.href, window.location.href);
                        const proxiedUrl = getProxiedUrl(formActionUrl);

                        if (proxiedUrl) {
                            form.setAttribute('action', proxiedUrl);
                        }
                        form.submit();
                    }
                }
            }, true); // Use capture phase to catch events early.

            function addFab() {
                const fabStyle = document.createElement('style');
                fabStyle.textContent = \`
                    .proxy-fab {
                        position: fixed;
                        bottom: 20px;
                        right: 20px;
                        width: 56px;
                        height: 56px;
                        background-color: #007bff;
                        color: white;
                        border-radius: 50%;
                        display: flex;
                        align-items: center;
                        justify-content: center;
                        font-size: 24px;
                        text-decoration: none;
                        box-shadow: 0 4px 8px rgba(0,0,0,0.2);
                        z-index: 9999;
                        border: none;
                        cursor: pointer;
                    }
                \`;
                document.head.appendChild(fabStyle);

                const fab = document.createElement('div');
                fab.className = 'proxy-fab';
                fab.title = 'Back to Proxy Home';
                const svgIcon = '<svg xmlns="http://www.w3.org/2000/svg" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 9 9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"></path><polyline points="9 22 9 12 15 12 15 22"></polyline></svg>';
                fab.innerHTML = svgIcon;
                
                fab.addEventListener('click', () => {
                    window.open('/', '_top');
                });
                document.body.appendChild(fab);
            }
            
            addFab();
        }

        if (document.readyState === 'loading') {
            document.addEventListener('DOMContentLoaded', onDomReady);
        } else {
            onDomReady();
        }
    })();
</script>
    `;
  },

  /**
   * Rewrites a Set-Cookie header string to use a reversed-domain path.
   * This simplified version creates a clean, session-only cookie.
   * @param {string} cookieHeader The original Set-Cookie header.
   * @param {string} requestHost The original host of the request.
   */
  rewriteCookie(cookieHeader, requestHost) {
      const parts = cookieHeader.split(';').map(p => p.trim());
      const keyValuePart = parts[0]; // "key=value"

      let domainPart = '';
      for (const part of parts.slice(1)) {
          if (part.toLowerCase().startsWith('domain=')) {
              domainPart = part.substring('domain='.length).trim();
              break;
          }
      }
      
      const cookieHost = domainPart || requestHost;
      const reversedHost = cookieHost.replace(/^\./, '').split('.').reverse().join('.');
      
      // Construct a new, simple session cookie with only the essential parts.
      // This discards original Path, Expires, Max-Age, Secure, HttpOnly, etc.
      return `${keyValuePart}; Path=/proxy/${reversedHost}/`;
  },

  /**
   * Serves the main index page which registers the service worker.
   */
  serveIndexPage() {
    const html = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Proxy</title>
    <style>
      body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; display: flex; flex-direction: column; align-items: center; padding-top: 5vh; margin: 0; background-color: #f0f2f5; color: #333; }
      #main-container { width: 90%; max-width: 600px; }
      #input-form { display: flex; margin-bottom: 2rem; }
      #url-input { flex-grow: 1; padding: 12px; font-size: 16px; border: 1px solid #ccc; border-radius: 8px 0 0 8px; }
      #go-button { padding: 12px 20px; font-size: 16px; border: 1px solid #007bff; background-color: #007bff; color: white; border-radius: 0 8px 8px 0; cursor: pointer; }
      #go-button:hover { background-color: #0056b3; }
      #bookmarks-container { width: 100%; }
      h2 { border-bottom: 2px solid #eee; padding-bottom: 10px; }
      ul { list-style: none; padding: 0; }
      li { display: flex; align-items: center; background-color: white; padding: 10px; margin-bottom: 8px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
      .bookmark-link { flex-grow: 1; text-decoration: none; color: #007bff; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
      .visit-count { margin: 0 10px; color: #666; font-size: 0.9em; }
      .delete-btn { background: none; border: none; color: #ff4d4d; cursor: pointer; font-size: 1.2em; }
    </style>
</head>
<body>
    <div id="main-container">
      <h1>Proxy</h1>
      <form id="input-form">
          <input type="text" id="url-input" placeholder="Enter a URL to visit, e.g., google.com" required>
          <button type="submit" id="go-button">Go</button>
      </form>
      
      <div id="bookmarks-container">
          <h2>Bookmarks</h2>
          <ul id="bookmarks-list"></ul>
      </div>
    </div>
    
    <script>
        const BOOKMARKS_KEY = 'proxy-bookmarks';
        const urlInput = document.getElementById('url-input');
        const inputForm = document.getElementById('input-form');
        const bookmarksList = document.getElementById('bookmarks-list');

        function getBookmarks() {
            try {
                const bookmarks = localStorage.getItem(BOOKMARKS_KEY);
                return bookmarks ? JSON.parse(bookmarks) : {};
            } catch (e) {
                console.error("Could not read bookmarks from localStorage", e);
                return {};
            }
        }

        function saveBookmarks(bookmarks) {
            localStorage.setItem(BOOKMARKS_KEY, JSON.stringify(bookmarks));
        }

        function renderBookmarks() {
            const bookmarks = getBookmarks();
            const sortedBookmarks = Object.entries(bookmarks).sort((a, b) => b[1].visits - a[1].visits);
            
            bookmarksList.innerHTML = ''; // Clear existing list

            for (const [url, data] of sortedBookmarks) {
                const li = document.createElement('li');
                
                const link = document.createElement('a');
                link.href = "#"; // The link itself doesn't navigate; the click handler does.
                link.textContent = url;
                link.className = 'bookmark-link';
                link.dataset.url = url;

                const visitCount = document.createElement('span');
                visitCount.className = 'visit-count';
                visitCount.textContent = \`(\${data.visits})\`;

                const deleteBtn = document.createElement('button');
                deleteBtn.className = 'delete-btn';
                deleteBtn.innerHTML = '&times;';
                deleteBtn.dataset.url = url;
                
                li.appendChild(link);
                li.appendChild(visitCount);
                li.appendChild(deleteBtn);
                bookmarksList.appendChild(li);
            }
        }

        function navigateToUrl(urlStr) {
             try {
                const targetUrl = new URL(urlStr);
                const reversedHost = targetUrl.hostname.split('.').reverse().join('.');
                const proxyUrl = \`/proxy/\${reversedHost}\${targetUrl.pathname}\${targetUrl.search}\`;
                window.location.href = proxyUrl;
            } catch(e) {
                alert("Invalid URL");
            }
        }

        function addOrUpdateBookmark(url) {
            const bookmarks = getBookmarks();
            if (!bookmarks[url]) {
                bookmarks[url] = { visits: 0 };
            }
            bookmarks[url].visits += 1;
            saveBookmarks(bookmarks);
            renderBookmarks();
        }

        inputForm.addEventListener('submit', (e) => {
            e.preventDefault();
            let urlStr = urlInput.value.trim();
            if (!urlStr) return;

            if (!urlStr.startsWith('http://') && !urlStr.startsWith('https://')) {
                urlStr = 'https://' + urlStr;
            }
            
            addOrUpdateBookmark(urlStr);
            navigateToUrl(urlStr);
        });

        bookmarksList.addEventListener('click', (e) => {
            e.preventDefault();
            if (e.target.matches('.bookmark-link')) {
                const url = e.target.dataset.url;
                addOrUpdateBookmark(url);
                navigateToUrl(url);
            } else if (e.target.matches('.delete-btn')) {
                const url = e.target.dataset.url;
                const bookmarks = getBookmarks();
                delete bookmarks[url];
                saveBookmarks(bookmarks);
                renderBookmarks();
            }
        });

        // Initial render
        renderBookmarks();

        if ('serviceWorker' in navigator) {
            navigator.serviceWorker.register('/sw.js?v=${SW_VERSION}')
                .then(reg => console.log('Service Worker registered.'))
                .catch(err => console.error('Service Worker registration failed:', err));
        }
    </script>
</body>
</html>`;
    return new Response(html, {
      headers: { 'Content-Type': 'text/html;charset=UTF-8' },
    });
  },

  /**
   * Serves the service worker JavaScript file.
   */
  serveServiceWorker() {
    const swCode = `
// Service Worker Version: ${SW_VERSION}

self.addEventListener('install', event => {
  // Activate the new service worker as soon as it's finished installing.
  // This avoids the need for the user to close all tabs.
  event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', event => {
  // Take control of all open pages (clients) at once.
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', event => {
    const requestUrl = new URL(event.request.url);

    // Exemption for cloudflareaccess.com domain
    if (requestUrl.hostname.endsWith('cloudflareaccess.com')) {
        return;
    }

    // Filter: If the request is for our own domain, check if we should ignore it.
    if (requestUrl.hostname === self.location.hostname) {
        // Ignore paths for the proxy's own assets and Cloudflare services.
        // Let the browser handle these directly.
        if (requestUrl.pathname === '/' ||
            requestUrl.pathname.startsWith('/sw.js') ||
            requestUrl.pathname.startsWith('/proxy/') ||
            requestUrl.pathname.startsWith('/cdn-cgi/')) {
            return; 
        }
    }

    // For all other requests, we take control.
    event.respondWith((async () => {

        // Case 1: The request is for an external domain.
        if (requestUrl.hostname !== self.location.hostname) {
            const reversedHost = requestUrl.hostname.split('.').reverse().join('.');
            const proxyUrlStr = \`/proxy/\${reversedHost}\${requestUrl.pathname}\${requestUrl.search}\`;
            
            console.log('[SW] Proxying external host:', requestUrl.href, '=>', proxyUrlStr);
            
            return fetch(new Request(proxyUrlStr, {
                method: event.request.method,
                headers: event.request.headers,
                body: event.request.body,
                redirect: 'manual',
                duplex: 'half'
            }));
        }

        // Case 2: A relative path from an already proxied page.
        // This is the only remaining case for our own domain.
        const client = await self.clients.get(event.clientId);
        if (client && client.url && new URL(client.url).pathname.startsWith('/proxy/')) {
            const clientUrl = new URL(client.url);
            const clientPathParts = clientUrl.pathname.substring('/proxy/'.length).split('/');
            const clientReversedHost = clientPathParts[0];

            const proxyUrlStr = \`/proxy/\${clientReversedHost}\${requestUrl.pathname}\${requestUrl.search}\`;
            
            console.log('[SW] Proxying relative path from proxied client:', event.request.url, '=>', proxyUrlStr);
            
            return fetch(new Request(proxyUrlStr, {
                method: event.request.method,
                headers: event.request.headers,
                body: event.request.body,
                redirect: 'manual',
                duplex: 'half'
            }));
        }

        // Fallback for any other unhandled requests to our own domain.
        console.warn('[SW] Blocking unhandled request to own domain:', requestUrl.href);
        return new Response("Not Found", { status: 404 });
    })());
});
`;
    return new Response(swCode.trim(), {
        headers: { 
            'Content-Type': 'application/javascript;charset=UTF-8',
            // Instruct browser to not cache the service worker file
            'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate',
        },
    });
  },
};
