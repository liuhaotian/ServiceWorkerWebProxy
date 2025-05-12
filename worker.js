// worker.js - Cloudflare Worker Script

// Content for the client-side Service Worker (sw.js)
// This will be served by the Cloudflare worker at the /sw.js path
const SERVICE_WORKER_JS = `
// sw.js - Client-side Service Worker

const PROXY_ENDPOINT = '/proxy?url='; // The endpoint in our Cloudflare worker
const SW_VERSION = '1.3.6'; // Updated for removing meta refresh tags

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

  // --- Step 0: Allow Cloudflare Access domains to pass through directly ---
  if (requestUrl.hostname.endsWith('.cloudflareaccess.com')) {
    console.log(\`SW (\${SW_VERSION}): Allowing Cloudflare Access request to pass through: \${request.url}\`);
    return; 
  }

  // --- Step 1: Exclude other non-proxyable requests ---
  if (requestUrl.pathname === '/sw.js' || 
      (requestUrl.origin === swOrigin && requestUrl.pathname === '/')) {
    return; 
  }

  // If the request is already for our /proxy endpoint (either initial nav, SW-proxied asset, or client-side rewritten link/form)
  if (requestUrl.origin === swOrigin && requestUrl.pathname.startsWith('/proxy')) {
    // console.log(\`SW (\${SW_VERSION}): Passing request to network (CF Worker): \${request.url}\`);
    return; // Let it go to the network (CF worker)
  }

  // --- Step 2: Determine the effective target URL for proxying ASSETS ---
  let effectiveTargetUrlString = request.url; 

  if (requestUrl.origin === swOrigin && event.clientId) { 
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
        }
      }
    } catch (e) {
      console.error(\`SW (\${SW_VERSION}): Error during relative ASSET path rebasing for \${request.url}. Error:\`, e);
    }
  }
  
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
// It handles click events on links, form submissions, and sets a cookie with the current proxied page's base URL.
const HTML_PAGE_PROXIED_CONTENT_SCRIPT = `
<script>
  // Script to run inside the proxied HTML content
  (function() {
    const PROXY_LAST_BASE_URL_COOKIE_NAME = 'proxy-last-base-url';

    // Function to get the original base URL of the currently displayed proxied page
    function getOriginalPageBaseUrl() {
      const proxyUrlParams = new URLSearchParams(window.location.search);
      return proxyUrlParams.get('url'); // This is the original URL
    }

    // Set a cookie with the current original page's base URL
    function setLastBaseUrlCookie() {
        const originalPageBase = getOriginalPageBaseUrl();
        if (originalPageBase) {
            const expires = new Date(Date.now() + 86400e3).toUTCString();
            const cookieValue = encodeURIComponent(originalPageBase);
            document.cookie = \`\${PROXY_LAST_BASE_URL_COOKIE_NAME}=\${cookieValue}; expires=\${expires}; path=/; SameSite=Lax\${window.location.protocol === 'https:' ? '; Secure' : ''}\`;
            console.log('Proxied Content Script: Set last base URL cookie to:', originalPageBase);
        }
    }
    
    setLastBaseUrlCookie(); 

    // Create and inject the "Proxy Home" link
    function addProxyHomeLink() {
      const homeLink = document.createElement('a');
      homeLink.id = 'proxy-home-link';
      homeLink.href = '/'; 
      homeLink.title = 'Proxy Home'; 
      const svgIcon = \`
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" fill="white" style="display: block; margin: auto;">
          <path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8h5z"/>
          <path d="M0 0h24v24H0z" fill="none"/>
        </svg>
      \`;
      homeLink.innerHTML = svgIcon;
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

    // --- Link Click Handler (acts as fallback or for dynamic content) ---
    document.addEventListener('click', function(event) {
      let anchorElement = event.target.closest('a');
      if (anchorElement) {
        if (anchorElement.id === 'proxy-home-link') {
          return; 
        }
        const href = anchorElement.getAttribute('href');
        if (href && (href.startsWith('/') || href.startsWith(window.location.origin)) && href.includes('/proxy?url=')) {
            return;
        }

        if (href && !href.startsWith('javascript:') && !href.startsWith('#')) {
          event.preventDefault(); 
          const originalPageBase = getOriginalPageBaseUrl();
          if (!originalPageBase) {
            console.error("Proxy Click Handler: Could not determine original page base URL for link:", href);
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            window.location.href = newProxyNavUrl;
            return;
          }
          try {
            const absoluteTargetUrl = new URL(href, originalPageBase).toString();
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(absoluteTargetUrl);
            window.location.href = newProxyNavUrl; 
          } catch (e) {
            console.error("Proxy Click Handler: Error resolving or navigating link:", href, e);
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            window.location.href = newProxyNavUrl;
          }
        }
      }
    }, true); 

    // --- Form Submission Handler (acts as fallback or for dynamic content) ---
    document.addEventListener('submit', function(event) {
        const form = event.target.closest('form');
        if (form) {
            const originalAction = form.getAttribute('action');
            if (originalAction && originalAction.includes('/proxy?url=')) {
                if ((form.getAttribute('method') || 'GET').toUpperCase() === 'POST') {
                } else {
                    return; 
                }
            }
            
            event.preventDefault(); 

            const originalPageBase = getOriginalPageBaseUrl();
            if (!originalPageBase) {
                console.error("Proxy Form Handler: Could not determine original page base URL for form submission.");
                form.submit(); 
                return;
            }

            let action = form.getAttribute('action') || '';
            const method = (form.getAttribute('method') || 'GET').toUpperCase();

            try {
                const absoluteActionUrl = new URL(action, originalPageBase).href;
                
                if (method === 'GET') {
                    const formData = new FormData(form);
                    const params = new URLSearchParams();
                    for (const pair of formData) {
                        params.append(pair[0], pair[1]);
                    }
                    const queryString = params.toString();
                    const finalTargetUrl = queryString ? \`\${absoluteActionUrl}?\${queryString}\` : absoluteActionUrl;
                    
                    const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(finalTargetUrl);
                    console.log('Proxy Form Handler (GET): Navigating to proxied URL:', newProxyNavUrl);
                    window.location.href = newProxyNavUrl;

                } else if (method === 'POST') {
                    const proxyPostUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(absoluteActionUrl);
                    
                    const newForm = document.createElement('form');
                    newForm.method = 'POST';
                    newForm.action = proxyPostUrl; 
                    
                    const formData = new FormData(form);
                    for (const pair of formData) {
                        const input = document.createElement('input');
                        input.type = 'hidden';
                        input.name = pair[0];
                        input.value = pair[1];
                        newForm.appendChild(input);
                    }
                    document.body.appendChild(newForm);
                    console.log('Proxy Form Handler (POST): Attempting to submit to proxy endpoint:', proxyPostUrl);
                    newForm.submit();

                } else {
                    console.warn(\`Proxy Form Handler: Unsupported form method "\${method}".\`);
                    form.submit(); 
                }
            } catch (e) {
                console.error("Proxy Form Handler: Error processing form submission:", e);
                form.submit(); 
            }
        }
    }, true); 

    console.log('Proxied Content Script: Initialized with base URL cookie setter, click, and form handlers.');
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
    <script src="/proxy?url=https%3A%2F%2Fcdn.tailwindcss.com"></script>
    <style>
        body {
            /* Standard system font stack */
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
        }
        .bookmark-item-content:hover .bookmark-name { 
            text-decoration: underline; 
        }
    </style>
</head>
<body class="bg-gradient-to-br from-slate-100 to-sky-100 flex flex-col items-center min-h-screen p-5 box-border">
    <div class="bg-white p-6 sm:p-8 rounded-xl shadow-2xl w-full max-w-xl text-center mb-5">
        <h1 class="text-3xl sm:text-4xl font-bold mb-6 sm:mb-8 text-slate-800">Service Worker Web Proxy</h1>
        <div>
            <label for="urlInput" class="block text-sm font-medium text-slate-700 mb-2 text-left">Enter URL to visit or select a bookmark:</label>
            <div class="flex mb-5">
                <input type="text" id="urlInput" placeholder="e.g., https://example.com"
                       class="flex-grow p-3 border border-slate-300 rounded-l-lg shadow-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500 text-base min-w-0">
                <button id="visitButton"
                        class="bg-indigo-600 hover:bg-indigo-700 text-white font-semibold p-3 rounded-r-lg shadow-md focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-indigo-500 whitespace-nowrap transition-colors duration-150">
                    Visit Securely
                </button>
            </div>
        </div>
        <div id="messageBox" class="text-sm text-red-600 min-h-[1.25em] mt-5"></div>
        <div id="swStatus" class="text-xs text-slate-500 mt-2">Initializing Service Worker...</div>
    </div>

    <div class="bookmarks-container bg-white p-6 sm:p-8 rounded-xl shadow-2xl w-full max-w-xl text-center mb-5">
        <h2 class="text-xl sm:text-2xl font-semibold mb-5 text-slate-700 text-left">Bookmarks <span class="text-sm font-normal text-slate-500">(Sorted by Visits)</span></h2>
        <ul id="bookmarksList" class="list-none p-0 m-0 text-left">
            </ul>
    </div>

    <div class="actions-container bg-white p-6 sm:p-8 rounded-xl shadow-2xl w-full max-w-xl text-center">
        <h2 class="text-xl sm:text-2xl font-semibold mb-5 text-slate-700 text-left">Proxy Actions</h2>
        <button id="clearDataButton"
                class="bg-amber-400 hover:bg-amber-500 text-slate-800 font-semibold py-3 px-4 rounded-lg shadow-md w-full focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-amber-500 transition-colors duration-150">
            Clear Proxy Data
        </button>
        <p class="text-xs text-slate-500 mt-3">
            Clears proxy-specific cookies, session storage, and service worker caches. Bookmarks are preserved.
        </p>
    </div>

    <script>
        const urlInput = document.getElementById('urlInput');
        const visitButton = document.getElementById('visitButton');
        const bookmarksList = document.getElementById('bookmarksList');
        const messageBox = document.getElementById('messageBox');
        const swStatus = document.getElementById('swStatus');
        const clearDataButton = document.getElementById('clearDataButton');
        const BOOKMARKS_LS_KEY = 'swProxyBookmarks_v2'; 

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
                li.className = 'text-slate-500 italic p-2'; 
                bookmarksList.appendChild(li);
                return;
            }
            bookmarks.forEach((bookmark) => { 
                const li = document.createElement('li');
                li.className = 'flex justify-between items-center py-3 border-b border-slate-200 last:border-b-0'; 

                const linkContent = document.createElement('div');
                linkContent.className = 'text-indigo-600 flex-grow mr-3 break-all cursor-pointer'; 
                linkContent.innerHTML = \`
                    <span class="bookmark-name font-medium block hover:underline">\${bookmark.name}</span>
                    <span class="bookmark-url text-xs text-slate-500 block">\${bookmark.url}</span>
                \`;
                linkContent.addEventListener('click', () => {
                    urlInput.value = bookmark.url;
                    visitButton.click(); 
                });
                
                const countSpan = document.createElement('span');
                countSpan.className = 'text-xs text-sky-600 ml-2 whitespace-nowrap'; 
                countSpan.textContent = \`Visits: \${bookmark.visitedCount}\`;
                
                const deleteBtn = document.createElement('button');
                deleteBtn.textContent = 'Delete';
                deleteBtn.className = 'bg-red-500 hover:bg-red-600 text-white text-xs py-1 px-2 rounded shadow focus:outline-none focus:ring-2 focus:ring-offset-1 focus:ring-red-500 transition-colors duration-150 ml-1'; 
                deleteBtn.addEventListener('click', () => { deleteBookmark(bookmark.url); });

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
                    try { bookmarkName = new URL(urlToVisit).hostname; } 
                    catch (e) { bookmarkName = urlToVisit; }
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

        async function clearProxyDataSelective() {
            messageBox.textContent = ''; 
            console.log('Clearing proxy data...');
            try {
                let bookmarksToKeep = localStorage.getItem(BOOKMARKS_LS_KEY);
                localStorage.clear(); 
                if (bookmarksToKeep) {
                    localStorage.setItem(BOOKMARKS_LS_KEY, bookmarksToKeep); 
                }
                console.log('LocalStorage (excluding bookmarks) cleared.');
                displayBookmarks(); 
                sessionStorage.clear();
                console.log('SessionStorage cleared.');
                const cookies = document.cookie.split(";");
                for (let i = 0; i < cookies.length; i++) {
                    const cookie = cookies[i];
                    const eqPos = cookie.indexOf("=");
                    const name = eqPos > -1 ? cookie.substr(0, eqPos) : cookie;
                    document.cookie = name + "=;expires=Thu, 01 Jan 1970 00:00:00 GMT;path=/;domain=" + window.location.hostname;
                    document.cookie = name + "=;expires=Thu, 01 Jan 1970 00:00:00 GMT;path=/"; 
                }
                console.log('Attempted to clear client-side accessible cookies for this proxy domain.');
                if ('serviceWorker' in navigator && window.caches) {
                    const cacheNames = await window.caches.keys();
                    for (const cacheName of cacheNames) {
                        await window.caches.delete(cacheName);
                        console.log('Cache deleted:', cacheName);
                    }
                    console.log('Service Worker caches cleared.');
                }
                console.log('Proxy data cleared. Bookmarks preserved.');
                window.location.reload();

            } catch (error) {
                console.error('Error clearing proxy data:', error);
                messageBox.textContent = 'Error during data clearing. See console.'; 
            }
        }

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
        
        clearDataButton.addEventListener('click', clearProxyDataSelective);
        urlInput.addEventListener('keypress', e => { if (e.key === 'Enter') { e.preventDefault(); visitButton.click(); }});

        displayBookmarks();
    </script>
</body>
</html>`;

// HTMLRewriter class to inject the client-side click handling script AND rewrite attributes
class AttributeRewriter {
  constructor(targetUrl, workerUrl) {
    this.targetUrl = targetUrl; 
    this.workerUrl = workerUrl; 
    this.attributesToRewrite = {
        'a': 'href', 'img': 'src', 'script': 'src',
        'link[rel="stylesheet"]': 'href', 'form': 'action',
        'iframe': 'src', 'audio': 'src', 'video': 'src',
        'source': 'src', 'track': 'src'   
    };
  }

  rewriteUrl(originalUrlValue) {
    if (!originalUrlValue || originalUrlValue.startsWith('data:') || originalUrlValue.startsWith('blob:') || originalUrlValue.startsWith('#') || originalUrlValue.startsWith('javascript:')) {
      return originalUrlValue; 
    }
    try {
      const absoluteTargetUrl = new URL(originalUrlValue, this.targetUrl.href).toString();
      return `${this.workerUrl}/proxy?url=${encodeURIComponent(absoluteTargetUrl)}`;
    } catch (e) {
      console.error(`Error rewriting URL value "${originalUrlValue}" against base "${this.targetUrl.href}": ${e.message}`);
      return originalUrlValue; 
    }
  }

  element(element) {
    const tagNameLower = element.tagName.toLowerCase();
    let attributeName = this.attributesToRewrite[tagNameLower];

    // Specific handling for <link rel="stylesheet">
    if (tagNameLower === 'link' && element.getAttribute('rel') === 'stylesheet') {
        attributeName = 'href';
    } 
    // Specific handling for <link rel="icon" ...> or similar
    else if (tagNameLower === 'link' && (element.getAttribute('rel') || '').includes('icon')) {
        attributeName = 'href';
    }
    // Specific handling for <meta http-equiv="refresh"> - NOW REMOVING IT
    else if (tagNameLower === 'meta' && (element.getAttribute('http-equiv') || '').toLowerCase() === 'refresh') {
        const content = element.getAttribute('content');
        if (content && content.toLowerCase().includes('url=')) {
            console.log(`Removing meta refresh tag: <meta http-equiv="refresh" content="${content}">`);
            element.remove();
        }
        return; // Handled meta refresh (by removing), no further attribute processing needed for this element
    }
    
    if (attributeName) {
      const originalValue = element.getAttribute(attributeName);
      if (originalValue) {
        const rewrittenValue = this.rewriteUrl(originalValue);
        if (rewrittenValue !== originalValue) {
          element.setAttribute(attributeName, rewrittenValue);
        }
      }
    }
  }
}

class ScriptInjector {
  constructor(scriptToInject) {
    this.scriptToInject = scriptToInject;
  }
  element(element) {
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
  const PROXY_LAST_BASE_URL_COOKIE_NAME = 'proxy-last-base-url'; // Define here for use in this function

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
        headers: filterRequestHeaders(request.headers, targetUrlObj, workerUrl), 
        body: (request.method !== 'GET' && request.method !== 'HEAD') ? request.body : null,
        redirect: 'manual'
    });

    try {
      let response = await fetch(outgoingRequest);
      let newResponseHeaders = new Headers(response.headers); 

      const setCookieHeaders = newResponseHeaders.getAll('Set-Cookie');
      newResponseHeaders.delete('Set-Cookie'); 
      for (const cookieHeader of setCookieHeaders) {
        let parts = cookieHeader.split(';').map(part => part.trim());
        parts = parts.filter(part => {
          const lowerPart = part.toLowerCase();
          return !lowerPart.startsWith('expires=') && !lowerPart.startsWith('max-age=');
        });
        newResponseHeaders.append('Set-Cookie', parts.join('; '));
      }

      if (response.status >= 300 && response.status < 400 && newResponseHeaders.has('location')) {
        let originalLocation = newResponseHeaders.get('location'); 
        if (response.headers.has('location')) { 
            originalLocation = response.headers.get('location');
        }
        let newLocation = new URL(originalLocation, targetUrlObj).toString(); 
        const proxiedRedirectUrl = `${workerUrl}/proxy?url=${encodeURIComponent(newLocation)}`;
        
        const redirectHeaders = new Headers();
        redirectHeaders.set('Location', proxiedRedirectUrl);
        for (const [key, value] of newResponseHeaders.entries()) {
            if (key.toLowerCase() !== 'location') { 
                 redirectHeaders.append(key, value);
            }
        }
        
        return new Response(response.body, { 
            status: response.status, 
            statusText: response.statusText, 
            headers: redirectHeaders 
        });
      }
      
      const targetOrigin = targetUrlObj.origin;
      // CSP with form-action allowing self and targetOrigin
      const cspPolicy = `default-src * data: blob: 'unsafe-inline' 'unsafe-eval'; script-src * data: blob: 'unsafe-inline' 'unsafe-eval'; form-action 'self' ${targetOrigin}; frame-src 'none'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';`;
      newResponseHeaders.set('Content-Security-Policy', cspPolicy);
      newResponseHeaders.delete('X-Frame-Options'); 
      newResponseHeaders.delete('Strict-Transport-Security'); 

      newResponseHeaders.set('Access-Control-Allow-Origin', '*'); 
      newResponseHeaders.set('Access-Control-Allow-Methods', 'GET, HEAD, POST, PUT, DELETE, OPTIONS');
      newResponseHeaders.set('Access-Control-Allow-Headers', 'Content-Type, Authorization, Range, X-Requested-With, Cookie');
      newResponseHeaders.set('Access-Control-Expose-Headers', 'Content-Length, Content-Range');
      newResponseHeaders.set('Access-Control-Allow-Credentials', 'true'); 

      const contentType = newResponseHeaders.get('Content-Type') || '';
      if (contentType.toLowerCase().includes('text/html') && response.ok && response.body) {
        const attributeRewriterInstance = new AttributeRewriter(targetUrlObj, workerUrl);
        const rewriter = new HTMLRewriter()
            .on('a', attributeRewriterInstance)
            .on('img', attributeRewriterInstance)
            .on('script', attributeRewriterInstance)
            .on('link[rel="stylesheet"]', attributeRewriterInstance) 
            .on('link[rel*="icon"]', attributeRewriterInstance) // For favicons
            .on('form', attributeRewriterInstance)
            .on('iframe', attributeRewriterInstance)
            .on('audio', attributeRewriterInstance)
            .on('video', attributeRewriterInstance)
            .on('source', attributeRewriterInstance)
            .on('track', attributeRewriterInstance)
            .on('meta', attributeRewriterInstance) // Ensure meta tags are processed by the rewriter
            .on('body', new ScriptInjector(HTML_PAGE_PROXIED_CONTENT_SCRIPT));
        
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

  // Fallback for unhandled relative paths using cookie
  if (url.pathname !== "/" && url.pathname !== "/sw.js" && !url.pathname.startsWith("/proxy")) {
    const lastBaseUrlCookieHeader = request.headers.get('Cookie');
    if (lastBaseUrlCookieHeader && lastBaseUrlCookieHeader.includes(PROXY_LAST_BASE_URL_COOKIE_NAME)) {
        const cookies = lastBaseUrlCookieHeader.split(';');
        let decodedLastBaseUrl = null;
        for (let cookie of cookies) {
            cookie = cookie.trim();
            if (cookie.startsWith(PROXY_LAST_BASE_URL_COOKIE_NAME + '=')) {
                try {
                    decodedLastBaseUrl = decodeURIComponent(cookie.substring(PROXY_LAST_BASE_URL_COOKIE_NAME.length + 1));
                    break;
                } catch (e) {
                    console.error("Error decoding last base URL cookie:", e);
                }
            }
        }

        if (decodedLastBaseUrl) {
            try {
                const fullIntendedUrl = new URL(url.pathname + url.search, decodedLastBaseUrl).toString();
                const redirectUrl = `${workerUrl}/proxy?url=${encodeURIComponent(fullIntendedUrl)}`;
                console.log(`Relative path fallback: redirecting from ${request.url} to ${redirectUrl} based on cookie.`);
                return Response.redirect(redirectUrl, 302);
            } catch(e) {
                console.error("Error constructing redirect URL from cookie-based fallback:", e);
            }
        }
    }
  }


  // Route 3: Serve the HTML landing page (input form)
  if (url.pathname === "/" || url.pathname === "/index.html" || url.pathname === "") {
    const landingPageHeaders = new Headers({ 'Content-Type': 'text/html;charset=UTF-8' });
    landingPageHeaders.set('Content-Security-Policy', 
        "default-src 'self'; " + 
        "script-src 'self' 'unsafe-inline'; " + 
        "style-src 'self' 'unsafe-inline'; " +  
        "font-src 'self' data:;" 
    );
    return new Response(HTML_PAGE_INPUT_FORM, { headers: landingPageHeaders });
  }

  // Route 4: 404 for everything else (if not handled by cookie fallback)
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
    const defaultReferer = targetUrlObj.origin + "/"; 
    const PROXY_LAST_BASE_URL_COOKIE_NAME = 'proxy-last-base-url'; 

    const headersToForwardGeneral = [
        'Accept', 'Accept-Charset', 'Accept-Encoding', 'Accept-Language',
        'Content-Type', 'Authorization', 'Range', 'X-Requested-With'
    ];

    for (const headerName of headersToForwardGeneral) {
        if (incomingHeaders.has(headerName)) {
            newHeaders.set(headerName, incomingHeaders.get(headerName));
        }
    }

    for (const [key, value] of incomingHeaders.entries()) {
        if (key.toLowerCase().startsWith('sec-ch-')) {
            newHeaders.set(key, value);
        }
    }
    
    const originalCookieHeader = incomingHeaders.get('cookie');
    if (originalCookieHeader) {
        const cookies = originalCookieHeader.split('; ');
        const filteredCookies = cookies.filter(cookie => {
            const cookieName = cookie.split('=')[0].trim(); 
            return !cookieName.toLowerCase().startsWith('cf_') && cookieName !== PROXY_LAST_BASE_URL_COOKIE_NAME;
        });
        if (filteredCookies.length > 0) {
            newHeaders.set('cookie', filteredCookies.join('; '));
        }
    }
    
    const incomingRefererString = incomingHeaders.get('Referer');
    if (incomingRefererString) {
        try {
            const incomingRefererUrl = new URL(incomingRefererString);
            if (incomingRefererUrl.origin === workerUrl && 
                incomingRefererUrl.pathname === '/proxy' &&
                incomingRefererUrl.searchParams.has('url')) {
                
                const previousProxiedPageUrl = decodeURIComponent(incomingRefererUrl.searchParams.get('url'));
                newHeaders.set('Referer', previousProxiedPageUrl); 
            } else if (incomingRefererUrl.origin !== workerUrl) {
                newHeaders.set('Referer', incomingRefererString);
            } else {
                newHeaders.set('Referer', defaultReferer);
            }
        } catch (e) {
            newHeaders.set('Referer', defaultReferer);
        }
    } else {
        newHeaders.set('Referer', defaultReferer);
    }

    const clientIp = incomingHeaders.get('CF-Connecting-IP');
    if (clientIp) {
        newHeaders.set('X-Forwarded-For', clientIp);
    }


    if (incomingHeaders.has('User-Agent')) {
        newHeaders.set('User-Agent', incomingHeaders.get('User-Agent'));
    } else {
        newHeaders.set('User-Agent', 'Cloudflare-Worker-ServiceWorker-Proxy/1.3.1'); 
    }
    
    const headersToRemove = ['cf-ipcountry', 'cf-ray', 'cf-visitor', 'x-forwarded-proto'];
    for(const header of headersToRemove){
        if(newHeaders.has(header)){
            newHeaders.delete(header);
        }
    }
    if (clientIp) { 
        newHeaders.delete('cf-connecting-ip');
    }
    
    return newHeaders;
}
