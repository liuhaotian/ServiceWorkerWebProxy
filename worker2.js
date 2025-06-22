/**
 * Cloudflare Worker: Path-Based Reverse Proxy with Service Worker Injection
 *
 * This worker implements a reverse proxy using a path-based routing scheme.
 * It uses a clever reversed-domain path to handle cross-subdomain cookies correctly.
 *
 * HOW IT WORKS:
 * 1.  A request to '/' serves an HTML page.
 * 2.  A request to '/sw.js' serves the service worker javascript, versioned for updates.
 * 3.  The HTML page registers the versioned '/sw.js' service worker. This worker intercepts
 * all subsequent navigation and fetch requests from the browser tab.
 * 4.  The Service Worker rewrites the request URL. For example, a request to
 * `https://google.com` is rewritten to `/proxy/com.google./` on the worker's domain.
 * It also intelligently handles relative path requests from already-proxied pages.
 * 5.  The Cloudflare worker receives the request at the `/proxy/...` endpoint.
 * 6.  It parses the reversed domain, un-reverses it to find the target host,
 * and forwards the request.
 * 7.  On response, it rewrites `Set-Cookie` and `Location` headers.
 * 8.  `Set-Cookie` `Domain` attributes are converted to `Path` attributes
 * using the same reversed-domain logic, preserving cross-subdomain behavior.
 * 9.  `Expires` and `Max-Age` are removed from cookies to make them session-only.
 */

// ===================================================================================
// Configuration & Main Worker Logic
// ===================================================================================

const SW_VERSION = '1.0.2'; // Increment to force service worker updates

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
    // e.g., /proxy/com.google.www/search?q=foo
    const pathSegments = url.pathname.substring("/proxy/".length).split('/');
    const reversedHost = pathSegments.shift(); // "com.google.www"
    const targetPath = "/" + pathSegments.join('/'); // "/search"

    // Un-reverse the hostname
    const targetHost = reversedHost.split('.').reverse().join('.'); // "www.google.com"
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
      const locationUrl = new URL(location, `https://${targetHost}`); // Resolve relative URLs
      const reversedLocationHost = locationUrl.hostname.split('.').reverse().join('.');
      const newLocation = `/proxy/${reversedLocationHost}${locationUrl.pathname}${locationUrl.search}`;
      responseHeaders.set('Location', newLocation);
    }
    
    // Remove headers that could break the proxy.
    responseHeaders.delete('Strict-Transport-Security');
    responseHeaders.delete('Content-Security-Policy');

    // 4. Return the modified response.
    return new Response(originResponse.body, {
      status: originResponse.status,
      statusText: originResponse.statusText,
      headers: responseHeaders,
    });
  },

  /**
   * Rewrites a Set-Cookie header string to use a reversed-domain path.
   * Also removes expiration to make it a session cookie.
   * @param {string} cookieHeader The original Set-Cookie header.
   * @param {string} requestHost The original host of the request.
   */
  rewriteCookie(cookieHeader, requestHost) {
      const parts = cookieHeader.split(';').map(p => p.trim());
      const newParts = [parts[0]]; // Keep the "key=value" part

      let domainPart = '';

      for (const part of parts.slice(1)) {
          const [key] = part.split('=');
          const lowerKey = key.toLowerCase();

          if (lowerKey === 'domain') {
              domainPart = part.substring(key.length + 1).trim();
              continue; // Will be replaced by Path
          }
          if (lowerKey === 'expires' || lowerKey === 'max-age') {
              continue; // Make cookies session-only
          }
          newParts.push(part);
      }
      
      const cookieHost = domainPart || requestHost;
      const reversedHost = cookieHost.replace(/^\./, '').split('.').reverse().join('.');
      
      newParts.push(`Path=/proxy/${reversedHost}/`);

      return newParts.join('; ');
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
      body { font-family: sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background-color: #f0f2f5; }
      #container { text-align: center; }
      #status { font-size: 1.2em; margin-bottom: 20px; }
      #version { font-size: 0.8em; color: #888; }
    </style>
</head>
<body>
    <div id="container">
      <h1 id="status">Proxy Service Worker is initializing...</h1>
      <p>Once active, all traffic from this tab will be proxied.</p>
      <p id="version">Version: ${SW_VERSION}</p>
    </div>
    <script>
        if ('serviceWorker' in navigator) {
            // Append version to SW URL to bypass cache and trigger update
            navigator.serviceWorker.register('/sw.js?v=${SW_VERSION}')
                .then(registration => {
                    console.log('Service Worker registered with scope:', registration.scope);
                    document.getElementById('status').textContent = 'Proxy Active';
                })
                .catch(error => {
                    console.error('Service Worker registration failed:', error);
                    document.getElementById('status').textContent = 'Proxy Failed to Start';
                });
        } else {
            document.getElementById('status').textContent = 'Service Workers are not supported in this browser.';
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
    event.respondWith((async () => {
        const requestUrl = new URL(event.request.url);

        // Case 1: The request is for an external domain (e.g., initial navigation).
        if (requestUrl.hostname !== self.location.hostname) {
            const reversedHost = requestUrl.hostname.split('.').reverse().join('.');
            const proxyUrlStr = \`/proxy/\${reversedHost}\${requestUrl.pathname}\${requestUrl.search}\`;
            
            console.log('[SW] Proxying external host:', requestUrl.href, '=>', proxyUrlStr);
            
            return fetch(new Request(proxyUrlStr, {
                method: event.request.method,
                headers: event.request.headers,
                body: event.request.body,
                redirect: 'manual',
            }));
        }

        // Case 2: The request is for our own domain.
        
        // If the path is already a proxy path, let it pass through. This is a critical
        // check to prevent re-wrapping an already correct URL, avoiding loops.
        if (requestUrl.pathname.startsWith('/proxy/')) {
            return fetch(event.request);
        }

        // It could be the proxy's own assets, or a relative path from a proxied page.
        const client = await self.clients.get(event.clientId);
        
        // If the request is coming from a page that is ALREADY proxied...
        if (client && client.url && new URL(client.url).pathname.startsWith('/proxy/')) {
            // ...then ANY path, including '/', is considered relative to the proxied site.
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
            }));
        }

        // If we are here, the request is for our own domain, from a non-proxied context
        // (like the root page itself loading). So we only serve our own assets.
        if (requestUrl.pathname === '/' || requestUrl.pathname.startsWith('/sw.js')) {
            return fetch(event.request);
        }

        // All other requests to our domain are considered invalid.
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
