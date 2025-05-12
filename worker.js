// worker.js - Cloudflare Worker Script

// Content for the client-side Service Worker (sw.js)
// This will be served by the Cloudflare worker at the /sw.js path
const SERVICE_WORKER_JS = `
// sw.js - Client-side Service Worker

const PROXY_ENDPOINT = '/proxy?url='; // The endpoint in our Cloudflare worker
const SW_VERSION = '1.2.4'; // Updated version for User-Agent change

// Install event
self.addEventListener('install', event => {
  console.log(\`Service Worker (\${SW_VERSION}): Installing...\`);
  event.waitUntil(self.skipWaiting());
});

// Activate event
self.addEventListener('activate', event => {
  console.log(\`Service Worker (\${SW_VERSION}): Activating...\`);
  event.waitUntil(self.clients.claim());
});

// Fetch event - intercept network requests
self.addEventListener('fetch', async event => { 
  const request = event.request;
  const requestUrl = new URL(request.url);
  const swOrigin = self.location.origin;

  // --- Step 1: Exclude non-proxyable requests ---
  if (requestUrl.pathname === '/sw.js' || 
      (requestUrl.origin === swOrigin && requestUrl.pathname === '/')) {
    // Let SW script and root page (input form) pass through
    return; 
  }

  // If the request is already for our /proxy endpoint (either initial nav, SW-proxied asset, or client-side rewritten link)
  if (requestUrl.origin === swOrigin && requestUrl.pathname.startsWith('/proxy')) {
    console.log(\`SW (\${SW_VERSION}): Passing request to network (CF Worker): \${request.url}\`);
    return; // Let it go to the network (CF worker)
  }

  // --- Step 2: Determine the effective target URL for proxying ASSETS ---
  let effectiveTargetUrlString = request.url; 

  if (requestUrl.origin === swOrigin && event.clientId) { // Asset requested from proxy's own domain
    try {
      const client = await self.clients.get(event.clientId); 
      if (client && client.url) {
        const clientPageProxyUrl = new URL(client.url); 
        if (clientPageProxyUrl.origin === swOrigin && 
            clientPageProxyUrl.pathname === '/proxy' && 
            clientPageProxyUrl.searchParams.has('url')) {
          
          const originalPageBaseUrlString = clientPageProxyUrl.searchParams.get('url');
          const rebasedAbsoluteUrl = new URL(requestUrl.pathname, originalPageBaseUrlString).toString();
          effectiveTargetUrlString = rebasedAbsoluteUrl;
          
          console.log(\`SW (\${SW_VERSION}): Rebased relative ASSET request. Original fetch: \${request.url}, Client page: \${client.url}, Rebased target: \${effectiveTargetUrlString}\`);
        }
      }
    } catch (e) {
      console.error(\`SW (\${SW_VERSION}): Error during relative ASSET path rebasing for \${request.url}. Error:\`, e);
    }
  }
  
  console.log(\`SW (\${SW_VERSION}): Final effective target for ASSET proxying: \${effectiveTargetUrlString}\`);
  const proxiedFetchUrl = swOrigin + PROXY_ENDPOINT + encodeURIComponent(effectiveTargetUrlString);
  
  const requestInit = {
      method: request.method,
      headers: request.headers, 
      mode: 'cors', 
      credentials: 'include', 
      cache: request.cache,
      redirect: 'manual', 
      referrer: request.referrer 
  };

  if (request.method !== 'GET' && request.method !== 'HEAD' && request.body) {
      event.respondWith(
          request.clone().arrayBuffer().then(body => {
              const newReq = new Request(proxiedFetchUrl, {...requestInit, body: body});
              return fetch(newReq);
          }).catch(err => {
              console.error(\`SW (\${SW_VERSION}): Error processing request body for \${effectiveTargetUrlString}:\`, err);
              return fetch(new Request(proxiedFetchUrl, requestInit));
          })
      );
  } else { 
      event.respondWith(fetch(new Request(proxiedFetchUrl, requestInit)));
  }
});
`;

// This script will be injected into HTML content served via /proxy
// It handles click events on links to ensure they go through the proxy, open in the current tab,
// and adds a "Proxy Home" link with an icon.
const HTML_PAGE_PROXIED_CONTENT_SCRIPT = `
<script>
  // Script to run inside the proxied HTML content
  (function() {
    // Function to get the original base URL of the currently displayed proxied page
    function getOriginalPageBaseUrl() {
      const proxyUrlParams = new URLSearchParams(window.location.search);
      return proxyUrlParams.get('url'); // This is the original URL
    }

    // Create and inject the "Proxy Home" link
    function addProxyHomeLink() {
      const homeLink = document.createElement('a');
      homeLink.id = 'proxy-home-link';
      homeLink.href = '/'; // Points to the root of the proxy worker
      homeLink.title = 'Proxy Home'; // Tooltip for accessibility

      // SVG Home Icon (simple, inline)
      const svgIcon = \`
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" fill="white" style="display: block; margin: auto;">
          <path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8h5z"/>
          <path d="M0 0h24v24H0z" fill="none"/>
        </svg>
      \`;
      homeLink.innerHTML = svgIcon;

      // Apply styles
      homeLink.style.position = 'fixed';
      homeLink.style.bottom = '15px'; 
      homeLink.style.left = '15px';  
      homeLink.style.zIndex = '2147483647'; 
      homeLink.style.backgroundColor = 'rgba(0, 123, 255, 0.7)'; 
      homeLink.style.width = '48px'; 
      homeLink.style.height = '48px';
      homeLink.style.display = 'flex';
      homeLink.style.alignItems = 'center';
      homeLink.style.justifyContent = 'center';
      homeLink.style.textDecoration = 'none';
      homeLink.style.borderRadius = '50%'; 
      homeLink.style.boxShadow = '0 4px 8px rgba(0,0,0,0.3)';
      homeLink.style.transition = 'background-color 0.2s ease-in-out, transform 0.1s ease-in-out';
      
      homeLink.addEventListener('mouseover', () => {
        homeLink.style.backgroundColor = 'rgba(0, 105, 217, 0.9)'; 
      });
      homeLink.addEventListener('mouseout', () => {
        homeLink.style.backgroundColor = 'rgba(0, 123, 255, 0.7)';
      });
       homeLink.addEventListener('mousedown', () => homeLink.style.transform = 'scale(0.95)');
       homeLink.addEventListener('mouseup', () => homeLink.style.transform = 'scale(1)');
      
      if (document.body) {
        document.body.appendChild(homeLink);
      } else {
        window.addEventListener('DOMContentLoaded', () => {
          if (document.body) {
            document.body.appendChild(homeLink);
          } else {
            console.error("Proxy Home Link: document.body not found even after DOMContentLoaded.");
          }
        });
      }
    }

    addProxyHomeLink(); 

    document.addEventListener('click', function(event) {
      let anchorElement = event.target.closest('a');

      if (anchorElement) {
        if (anchorElement.id === 'proxy-home-link') {
          console.log('Proxy Home link clicked, navigating to /');
          return; 
        }

        const href = anchorElement.getAttribute('href');
        
        if (href && !href.startsWith('javascript:') && !href.startsWith('#')) {
          event.preventDefault(); 

          const originalPageBase = getOriginalPageBaseUrl();
          if (!originalPageBase) {
            console.error("Proxy Click Handler: Could not determine original page base URL for link:", href);
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            console.warn('Proxy Click Handler: Original page base URL missing. Attempting to proxy href directly:', newProxyNavUrl);
            window.location.href = newProxyNavUrl;
            return;
          }

          try {
            const absoluteTargetUrl = new URL(href, originalPageBase).toString();
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(absoluteTargetUrl);
            console.log('Proxy Click Handler: Navigating to proxied URL (current tab):', newProxyNavUrl);
            window.location.href = newProxyNavUrl; 
          } catch (e) {
            console.error("Proxy Click Handler: Error resolving or navigating link:", href, e);
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            console.warn('Proxy Click Handler: Error during URL resolution. Attempting to proxy href directly:', newProxyNavUrl);
            window.location.href = newProxyNavUrl;
          }
        }
      }
    }, true); 

    console.log('Proxied Content Script: Click handler (current tab enforced) and Home link initialized.');
  })();
</script>
`;


// Define the HTML content for the landing page (input form)
const HTML_PAGE_INPUT_FORM = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Service Worker Web Proxy</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
            background: #f0f2f5; display: flex; flex-direction: column; align-items: center;
            min-height: 100vh; margin: 0; padding: 20px; box-sizing: border-box;
        }
        .container, .bookmarks-container {
            background-color: #ffffff; padding: 25px; border-radius: 12px;
            box-shadow: 0 8px 20px rgba(0, 0, 0, 0.08); width: 100%;
            max-width: 550px; text-align: center; margin-bottom: 20px;
        }
        h1 { font-size: 26px; font-weight: 700; margin-bottom: 25px; color: #333333; }
        h2 { font-size: 20px; font-weight: 600; margin-top: 0; margin-bottom: 20px; color: #444444; text-align: left;}
        label {
            display: block; font-size: 14px; font-weight: 500; color: #555555;
            margin-bottom: 8px; text-align: left;
        }
        .input-group { display: flex; margin-bottom: 20px; } /* Increased margin-bottom */
        input[type="text"]#urlInput {
            flex-grow: 1;
            padding: 12px 16px; border: 1px solid #dddddd;
            border-radius: 8px 0 0 8px; 
            box-shadow: inset 0 1px 2px rgba(0,0,0,0.05);
            font-size: 16px;
            box-sizing: border-box;
            min-width: 0; 
        }
        input[type="text"]#urlInput:focus {
            outline: none; border-color: #007bff;
            box-shadow: 0 0 0 3px rgba(0, 123, 255, 0.25); z-index: 1;
        }
        button {
            background-color: #007bff; color: white; font-weight: 600;
            padding: 12px 16px; border: none;
            cursor: pointer; font-size: 16px;
            transition: background-color 0.2s ease-in-out, transform 0.1s ease-in-out;
        }
        button#visitButton {
            border-radius: 0 8px 8px 0; 
             white-space: nowrap; 
        }
        /* Removed addBookmarkButton styling as the button is removed */
        button:hover { background-color: #0056b3; }
        button:active { transform: translateY(1px); }

        .message-box { margin-top: 20px; font-size: 14px; color: #dc3545; min-height: 1.25em; }
        .sw-status { margin-top: 10px; font-size: 12px; color: #666; }

        /* Bookmarks styling */
        #bookmarksList { list-style: none; padding: 0; margin: 0; text-align: left; }
        #bookmarksList li {
            display: flex; justify-content: space-between; align-items: center;
            padding: 10px; border-bottom: 1px solid #eee;
            font-size: 15px;
        }
        #bookmarksList li:last-child { border-bottom: none; }
        /* Removed a.bookmark-link class as div is used now */
        #bookmarksList .bookmark-item-content { /* Class for the clickable div */
            color: #007bff; text-decoration: none; flex-grow: 1;
            margin-right: 10px; word-break: break-all; cursor: pointer;
        }
        #bookmarksList .bookmark-item-content:hover .bookmark-name { text-decoration: underline; } /* Underline name on hover */
        #bookmarksList .bookmark-name { font-weight: 500; display: block; margin-bottom: 3px; }
        #bookmarksList .bookmark-url { font-size: 0.85em; color: #6c757d; }
        #bookmarksList .bookmark-count { font-size: 0.8em; color: #17a2b8; margin-left: 8px; white-space: nowrap; }


        #bookmarksList button.delete-bookmark {
            background-color: #dc3545; color: white;
            border: none; border-radius: 5px;
            padding: 5px 10px; font-size: 12px; cursor: pointer;
            transition: background-color 0.2s ease-in-out;
            margin-left: 5px; /* Add some space before delete button */
        }
        #bookmarksList button.delete-bookmark:hover { background-color: #c82333; }
        .no-bookmarks { color: #6c757d; font-style: italic; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Service Worker Web Proxy</h1>
        <div>
            <label for="urlInput">Enter URL to visit or select a bookmark:</label>
            <div class="input-group">
                <input type="text" id="urlInput" placeholder="e.g., https://example.com">
                <button id="visitButton">Visit Securely</button>
            </div>
        </div>
        <div id="messageBox" class="message-box"></div>
        <div id="swStatus" class="sw-status">Initializing Service Worker...</div>
    </div>

    <div class="bookmarks-container">
        <h2>Bookmarks (Sorted by Visits)</h2>
        <ul id="bookmarksList">
            </ul>
    </div>

    <script>
        // Script for the main input form page
        const urlInput = document.getElementById('urlInput');
        const visitButton = document.getElementById('visitButton');
        const bookmarksList = document.getElementById('bookmarksList');
        const messageBox = document.getElementById('messageBox');
        const swStatus = document.getElementById('swStatus');
        const BOOKMARKS_LS_KEY = 'swProxyBookmarks_v2'; // Changed key for new structure

        function getBookmarks() {
            const bookmarksJson = localStorage.getItem(BOOKMARKS_LS_KEY);
            let bookmarks = bookmarksJson ? JSON.parse(bookmarksJson) : [];
            return bookmarks.map(bm => ({
                name: bm.name || bm.url, 
                url: bm.url,
                visitedCount: bm.visitedCount || 0 
            }));
        }

        function saveBookmarks(bookmarks) {
            localStorage.setItem(BOOKMARKS_LS_KEY, JSON.stringify(bookmarks));
        }

        function displayBookmarks() {
            let bookmarks = getBookmarks();
            bookmarks.sort((a, b) => b.visitedCount - a.visitedCount);
            
            bookmarksList.innerHTML = ''; 

            if (bookmarks.length === 0) {
                const li = document.createElement('li');
                li.textContent = 'No bookmarks saved yet. Visit a URL to add it automatically.';
                li.classList.add('no-bookmarks');
                bookmarksList.appendChild(li);
                return;
            }

            bookmarks.forEach((bookmark) => { // Removed index as it's not strictly needed for delete by URL
                const li = document.createElement('li');
                
                const linkContent = document.createElement('div');
                linkContent.classList.add('bookmark-item-content'); // Added class for styling
                linkContent.innerHTML = \`
                    <span class="bookmark-name">\${bookmark.name}</span>
                    <span class="bookmark-url">\${bookmark.url}</span>
                \`;
                linkContent.addEventListener('click', () => {
                    urlInput.value = bookmark.url;
                    visitButton.click(); // Automatically click "Visit Securely"
                });
                
                const countSpan = document.createElement('span');
                countSpan.classList.add('bookmark-count');
                countSpan.textContent = \`Visits: \${bookmark.visitedCount}\`;
                
                const deleteBtn = document.createElement('button');
                deleteBtn.textContent = 'Delete';
                deleteBtn.classList.add('delete-bookmark');
                deleteBtn.addEventListener('click', () => {
                    deleteBookmark(bookmark.url); 
                });

                li.appendChild(linkContent);
                li.appendChild(countSpan);
                li.appendChild(deleteBtn);
                bookmarksList.appendChild(li);
            });
        }

        function addOrUpdateBookmark(urlToVisit, name) {
            let bookmarks = getBookmarks();
            const existingBookmarkIndex = bookmarks.findIndex(bm => bm.url === urlToVisit);

            if (existingBookmarkIndex > -1) {
                bookmarks[existingBookmarkIndex].visitedCount += 1;
                if (name && name !== bookmarks[existingBookmarkIndex].name) { 
                    bookmarks[existingBookmarkIndex].name = name;
                }
            } else {
                let bookmarkName = name;
                if (!bookmarkName) {
                    try {
                        bookmarkName = new URL(urlToVisit).hostname;
                    } catch (e) {
                        bookmarkName = urlToVisit; 
                    }
                }
                bookmarks.push({ name: bookmarkName, url: urlToVisit, visitedCount: 1 });
            }
            saveBookmarks(bookmarks);
            displayBookmarks(); 
        }
        
        function deleteBookmark(urlToDelete) {
            let bookmarks = getBookmarks();
            bookmarks = bookmarks.filter(bm => bm.url !== urlToDelete);
            saveBookmarks(bookmarks);
            displayBookmarks();
            messageBox.textContent = 'Bookmark deleted.';
            setTimeout(() => messageBox.textContent = '', 2000);
        }

        // Service Worker Registration
        if ('serviceWorker' in navigator) {
            window.addEventListener('load', () => {
                navigator.serviceWorker.register('/sw.js', { scope: '/' })
                    .then(registration => {
                        swStatus.textContent = 'Service Worker active.';
                        if (!navigator.serviceWorker.controller) {
                             swStatus.textContent = 'Service Worker registered. May need reload to fully activate.';
                        }
                    })
                    .catch(error => {
                        console.error('ServiceWorker registration failed: ', error);
                        swStatus.textContent = 'Service Worker registration failed.';
                        messageBox.textContent = 'Proxy limited: SW registration failed.';
                    });
            });
        } else {
            swStatus.textContent = 'Service Workers not supported.';
            messageBox.textContent = 'Proxy limited: Service Workers not supported.';
        }

        // Event Listeners
        visitButton.addEventListener('click', () => {
            let destUrl = urlInput.value.trim();
            messageBox.textContent = '';
            if (!destUrl) { messageBox.textContent = 'Please enter a URL.'; return; }
            
            let fullDestUrl = destUrl;
            if (!fullDestUrl.startsWith('http://') && !fullDestUrl.startsWith('https://')) {
                fullDestUrl = 'https://' + fullDestUrl;
            }

            try {
                new URL(fullDestUrl); 
                addOrUpdateBookmark(fullDestUrl); 
                window.location.href = window.location.origin + '/proxy?url=' + encodeURIComponent(fullDestUrl);
            } catch (e) { 
                messageBox.textContent = 'Invalid URL format.'; 
            }
        });

        urlInput.addEventListener('keypress', e => { if (e.key === 'Enter') { e.preventDefault(); visitButton.click(); }});

        // Initial load of bookmarks
        displayBookmarks();
    </script>
</body>
</html>`;

// HTMLRewriter class to inject the client-side click handling script
class ScriptInjector {
  constructor(scriptToInject) {
    this.scriptToInject = scriptToInject;
  }
  element(element) {
    // Append the script to the end of the <body> tag
    element.append(this.scriptToInject, { html: true });
  }
}


// Add event listener for 'fetch' events
addEventListener('fetch', event => {
  event.respondWith(handleRequest(event.request)); 
});

/**
 * Handles incoming requests for the Cloudflare Worker.
 * @param {Request} request - The incoming request object
 */
async function handleRequest(request) {
  const url = new URL(request.url);
  const workerUrl = url.origin;

  // Route 1: Serve the Service Worker JavaScript file
  if (url.pathname === "/sw.js") {
    return new Response(SERVICE_WORKER_JS, {
      headers: { 
        'Content-Type': 'application/javascript;charset=UTF-8',
        'Service-Worker-Allowed': '/' 
      },
    });
  }

  // Route 2: Handle proxy requests (from initial navigation or from Service Worker)
  if (url.pathname === "/proxy") {
    const targetUrlString = url.searchParams.get("url");
    if (!targetUrlString) return new Response("Missing 'url' query parameter.", { status: 400, headers: {'Content-Type': 'text/plain'} });

    let targetUrlObj;
    try {
        targetUrlObj = new URL(targetUrlString);
    } catch (e) {
        return new Response(`Invalid target URL: "${targetUrlString}". ${e.message}`, { status: 400, headers: {'Content-Type': 'text/plain'} });
    }

    const outgoingRequest = new Request(targetUrlObj.toString(), {
        method: request.method,
        headers: filterRequestHeaders(request.headers, targetUrlObj, workerUrl), // Pass targetUrlObj for referer
        body: (request.method !== 'GET' && request.method !== 'HEAD') ? request.body : null,
        redirect: 'manual'
    });

    try {
      let response = await fetch(outgoingRequest);
      let newResponseHeaders = new Headers(response.headers); 

      if (response.status >= 300 && response.status < 400 && newResponseHeaders.has('location')) {
        let originalLocation = newResponseHeaders.get('location');
        let newLocation = new URL(originalLocation, targetUrlObj).toString(); 
        const proxiedRedirectUrl = `${workerUrl}/proxy?url=${encodeURIComponent(newLocation)}`;
        newResponseHeaders.set('Location', proxiedRedirectUrl);
        return new Response(response.body, { 
            status: response.status, 
            statusText: response.statusText, 
            headers: newResponseHeaders 
        });
      }
      
      const relaxedCSPWithNoIframes = "default-src * data: blob: 'unsafe-inline' 'unsafe-eval'; script-src * data: blob: 'unsafe-inline' 'unsafe-eval'; frame-src 'none'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';";
      newResponseHeaders.set('Content-Security-Policy', relaxedCSPWithNoIframes);
      newResponseHeaders.delete('X-Frame-Options'); 
      newResponseHeaders.delete('Strict-Transport-Security'); 

      newResponseHeaders.set('Access-Control-Allow-Origin', '*'); 
      newResponseHeaders.set('Access-Control-Allow-Methods', 'GET, HEAD, POST, PUT, DELETE, OPTIONS');
      newResponseHeaders.set('Access-Control-Allow-Headers', 'Content-Type, Authorization, Range, X-Requested-With, Cookie');
      newResponseHeaders.set('Access-Control-Expose-Headers', 'Content-Length, Content-Range');
      newResponseHeaders.set('Access-Control-Allow-Credentials', 'true'); 

      const contentType = newResponseHeaders.get('Content-Type') || '';
      // If the content is HTML, inject the client-side click handling script
      if (contentType.toLowerCase().includes('text/html') && response.ok && response.body) {
        const rewriter = new HTMLRewriter().on('body', new ScriptInjector(HTML_PAGE_PROXIED_CONTENT_SCRIPT));
        
        const transformedBody = rewriter.transform(response).body;

        return new Response(transformedBody, {
            status: response.status,
            statusText: response.statusText,
            headers: newResponseHeaders 
        });
      }

      return new Response(response.body, {
        status: response.status,
        statusText: response.statusText,
        headers: newResponseHeaders
      });

    } catch (e) {
      console.error(`Cloudflare Worker: Error fetching ${targetUrlString}: ${e.message}`, e);
      return new Response(`Cloudflare Worker: Error fetching target URL. ${e.message}`, { status: 502, headers: {'Content-Type': 'text/plain'} });
    }
  }

  // Route 3: Serve the HTML landing page (input form)
  if (url.pathname === "/" || url.pathname === "/index.html" || url.pathname === "") {
    const landingPageHeaders = new Headers({ 'Content-Type': 'text/html;charset=UTF-8' });
    landingPageHeaders.set('Content-Security-Policy', "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline';");
    return new Response(HTML_PAGE_INPUT_FORM, { headers: landingPageHeaders });
  }

  // Route 4: 404 for everything else
  return new Response("Resource Not Found.", { status: 404, headers: {'Content-Type': 'text/plain'} });
}

/**
 * Filters and constructs headers for the outgoing request to the target server.
 * @param {Headers} incomingHeaders - Headers from the client's request to the worker OR from SW to worker.
 * @param {URL} targetUrlObj - The URL object of the target URL.
 * @param {string} workerUrl - The origin of the worker itself (e.g., "https://proxy.workers.dev").
 * @returns {Headers} A new Headers object for the outgoing request.
 */
function filterRequestHeaders(incomingHeaders, targetUrlObj, workerUrl) {
    const newHeaders = new Headers();
    const defaultReferer = targetUrlObj.origin + "/"; // Default referer is the origin of the target URL

    // Standard headers to generally forward, EXCLUDING User-Agent initially
    const headersToForwardGeneral = [
        'Accept', 'Accept-Charset', 'Accept-Encoding', 'Accept-Language',
        /* 'User-Agent', // Handled below */
        'Content-Type', 'Authorization', 'Range', 'X-Requested-With'
    ];

    for (const headerName of headersToForwardGeneral) {
        if (incomingHeaders.has(headerName)) {
            newHeaders.set(headerName, incomingHeaders.get(headerName));
        }
    }

    // Forward Sec-CH-* (Client Hints) headers if present
    for (const [key, value] of incomingHeaders.entries()) {
        if (key.toLowerCase().startsWith('sec-ch-')) {
            newHeaders.set(key, value);
        }
    }
    
    // Handle Cookie header: filter out CF_ prefixed cookies
    const originalCookieHeader = incomingHeaders.get('cookie');
    if (originalCookieHeader) {
        const cookies = originalCookieHeader.split('; ');
        const filteredCookies = cookies.filter(cookie => {
            const cookieName = cookie.split('=')[0];
            return !cookieName.toLowerCase().startsWith('cf_');
        });
        if (filteredCookies.length > 0) {
            newHeaders.set('cookie', filteredCookies.join('; '));
            // console.log("Forwarding filtered cookies:", filteredCookies.join('; '));
        }
    }
    
    // Refined Referer Logic:
    const incomingRefererString = incomingHeaders.get('Referer');
    if (incomingRefererString) {
        try {
            const incomingRefererUrl = new URL(incomingRefererString);
            // Check if the referer is from one of our proxied pages
            if (incomingRefererUrl.origin === workerUrl && 
                incomingRefererUrl.pathname === '/proxy' &&
                incomingRefererUrl.searchParams.has('url')) {
                
                const previousProxiedPageUrl = decodeURIComponent(incomingRefererUrl.searchParams.get('url'));
                newHeaders.set('Referer', previousProxiedPageUrl); 
            } else if (incomingRefererUrl.origin !== workerUrl) {
                // If referer is external (not our proxy), forward it as is
                newHeaders.set('Referer', incomingRefererString);
            } else {
                // If referer is our proxy's root or some other internal page, use default
                newHeaders.set('Referer', defaultReferer);
            }
        } catch (e) {
            // If parsing incomingRefererString fails, fall back to default
            newHeaders.set('Referer', defaultReferer);
        }
    } else {
        // No incoming referer, set to default (target's origin)
        newHeaders.set('Referer', defaultReferer);
    }

    // Handle User-Agent: Prioritize incoming User-Agent from the client/SW
    if (incomingHeaders.has('User-Agent')) {
        newHeaders.set('User-Agent', incomingHeaders.get('User-Agent'));
    } else {
        // Fallback if no User-Agent is present in the incoming request (should be rare for browser/SW)
        newHeaders.set('User-Agent', 'Cloudflare-Worker-ServiceWorker-Proxy/1.2.4'); // Updated version
    }
    
    // Remove Cloudflare-internal headers that might have been added by the CF network
    for (let key of newHeaders.keys()) { 
        if (key.toLowerCase().startsWith('cf-')) {
            newHeaders.delete(key);
        }
    }
    newHeaders.delete('X-Forwarded-For');
    newHeaders.delete('X-Forwarded-Proto');
    
    return newHeaders;
}
