// worker.js - Cloudflare Worker Script

// Content for the client-side Service Worker (sw.js)
// This will be served by the Cloudflare worker at the /sw.js path
const SERVICE_WORKER_JS = `
// sw.js - Client-side Service Worker

const PROXY_ENDPOINT = '/proxy?url='; // Cloudflare worker's proxy endpoint
const SW_VERSION = '1.5.2'; 

// Service Worker Install Event
self.addEventListener('install', event => {
  // Ensures the new service worker activates immediately
  event.waitUntil(self.skipWaiting());
});

// Service Worker Activate Event
self.addEventListener('activate', event => {
  // Ensures the new service worker takes control of all clients immediately
  event.waitUntil(self.clients.claim());
});

// Service Worker Fetch Event - intercepts network requests
self.addEventListener('fetch', async event => { 
  const request = event.request;
  const requestUrl = new URL(request.url);
  const swOrigin = self.location.origin; // Origin of the service worker (the proxy domain)

  // Ignore requests for Cloudflare Access authentication URLs to prevent interference
  if (requestUrl.hostname.endsWith('.cloudflareaccess.com')) {
    return; // Let these requests pass through normally
  }

  // Don't proxy the service worker script itself or the root page request from the browser
  if (requestUrl.pathname === '/sw.js' || 
      (requestUrl.origin === swOrigin && requestUrl.pathname === '/')) {
    return; // Let the main Cloudflare worker handle these
  }

  // Don't re-proxy requests already directed to our /proxy endpoint
  if (requestUrl.origin === swOrigin && requestUrl.pathname.startsWith('/proxy')) {
    return; // Let the main Cloudflare worker handle these
  }

  let effectiveTargetUrlString = request.url; 

  // For requests originating from the same origin as the SW (e.g., assets on the proxied page),
  // attempt to rebase their URLs against the original page's base URL.
  // This handles relative paths for assets loaded by the proxied page.
  if (requestUrl.origin === swOrigin && event.clientId) { 
    try {
      const client = await self.clients.get(event.clientId); // Get the client (window) that made the request
      if (client && client.url) {
        const clientPageProxyUrl = new URL(client.url); // URL of the page in the browser (e.g., https://proxy.com/proxy?url=http://original.com/page.html)
        
        // Check if the client URL is indeed a proxied page
        if (clientPageProxyUrl.origin === swOrigin && 
            clientPageProxyUrl.pathname === '/proxy' && 
            clientPageProxyUrl.searchParams.has('url')) {
          
          const originalPageBaseUrlString = clientPageProxyUrl.searchParams.get('url'); // e.g., http://original.com/page.html
          // Resolve the relative path from the request (e.g., /image.jpg) against the original page's full URL.
          const rebasedAbsoluteUrl = new URL(requestUrl.pathname, originalPageBaseUrlString).toString();
          effectiveTargetUrlString = rebasedAbsoluteUrl;
        }
      }
    } catch (e) {
      console.error(\`SW (\${SW_VERSION}): Error during relative ASSET path rebasing for \${request.url}. Client URL: \${event.clientId ? (await self.clients.get(event.clientId))?.url : 'N/A'}. Error:\`, e);
      // If rebasing fails, proceed with the original request.url; the main worker might handle it or it might fail.
    }
  }
  
  // Construct the URL to fetch from our Cloudflare worker's proxy endpoint
  const proxiedFetchUrl = swOrigin + PROXY_ENDPOINT + encodeURIComponent(effectiveTargetUrlString);
  
  // Prepare the request options for fetching through the proxy
  const requestInit = {
      method: request.method,
      headers: request.headers,    // Forward original headers
      mode: 'cors',                // Required for cross-origin requests via SW
      credentials: 'include',      // Forward cookies
      cache: request.cache,        // Respect original cache settings
      redirect: 'manual',          // The main Cloudflare worker will handle redirects and rewrite Location headers
      referrer: request.referrer   // Forward original referrer
  };

  // For requests with a body (POST, PUT, etc.), clone the body and include it.
  if (request.method !== 'GET' && request.method !== 'HEAD' && request.body) {
      event.respondWith(
          request.clone().arrayBuffer().then(body => {
              const newReq = new Request(proxiedFetchUrl, {...requestInit, body: body});
              return fetch(newReq);
          }).catch(err => {
              console.error(\`SW (\${SW_VERSION}): Error processing request body for \${effectiveTargetUrlString}:\`, err);
              // Fallback if body processing fails (e.g., if body already consumed or unreadable)
              return fetch(new Request(proxiedFetchUrl, requestInit));
          })
      );
  } else { 
      // For GET/HEAD requests (or other bodyless requests)
      event.respondWith(fetch(new Request(proxiedFetchUrl, requestInit)));
  }
});
`;

// This script will be injected into HTML content served via /proxy.
// The ScriptInjector class will wrap this with a <script> tag, potentially with a nonce.
const HTML_PAGE_PROXIED_CONTENT_SCRIPT = `
  // Script to run inside the proxied HTML content
  (function() {
    const PROXY_LAST_BASE_URL_COOKIE_NAME = 'proxy-last-base-url';

    function getOriginalPageBaseUrl() {
      const proxyUrlParams = new URLSearchParams(window.location.search);
      return proxyUrlParams.get('url'); 
    }

    function setLastBaseUrlCookie() {
        const originalPageBase = getOriginalPageBaseUrl();
        if (originalPageBase) {
            const expires = new Date(Date.now() + 86400e3).toUTCString();
            const cookieValue = encodeURIComponent(originalPageBase);
            document.cookie = \`\${PROXY_LAST_BASE_URL_COOKIE_NAME}=\${cookieValue}; expires=\${expires}; path=/; SameSite=Lax\${window.location.protocol === 'https:' ? '; Secure' : ''}\`;
        }
    }
    
    setLastBaseUrlCookie(); 

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
          }
        });
      }
    }

    addProxyHomeLink(); 

    document.addEventListener('click', function(event) {
      let anchorElement = event.target.closest('a');
      if (anchorElement) {
        // Client-side handling for target="_blank" (also handled by HTMLRewriter now, this acts as a fallback or for dynamic links)
        const originalTarget = anchorElement.getAttribute('target');
        if (originalTarget && originalTarget.toLowerCase() === '_blank') {
            anchorElement.target = '_self';
        }

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
            console.error("Proxy Click Handler (Client-Side Rewrite): Error resolving or navigating link:", href, e);
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            window.location.href = newProxyNavUrl;
          }
        }
      }
    }, true); 

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
                    newForm.submit();

                } else {
                    form.submit(); 
                }
            } catch (e) {
                form.submit(); 
            }
        }
    }, true); 
  })();
`;


// Define the HTML content for the landing page (input form)
// The {{NONCE_MAIN_PAGE}} placeholder will be replaced by the Cloudflare Worker.
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
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
        }
        .bookmark-item-content:hover .bookmark-name { 
            text-decoration: underline; 
        }
        .checkbox-label {
            display: flex;
            align-items: center;
            margin-bottom: 0.75rem; /* mb-3 */
            font-size: 0.875rem; /* text-sm */
            color: #4a5568; /* text-slate-700 */
            justify-content: flex-start;
        }
        .checkbox-label input[type="checkbox"] {
            margin-right: 0.5rem; /* mr-2 */
            height: 1rem; /* h-4 */
            width: 1rem; /* w-4 */
            border-radius: 0.25rem; /* rounded */
            border-color: #cbd5e1; /* border-slate-300 */
        }
        .checkbox-label input[type="checkbox"]:focus {
            ring: 2px;
            ring-color: #6366f1; /* focus:ring-indigo-500 */
        }
        /* Styles for collapsible advanced settings */
        details > summary {
            list-style: none; /* Remove default marker */
            cursor: pointer;
            font-weight: 500;
            color: #4a5568; /* text-slate-600 */
            padding: 0.5rem 0;
            margin-bottom: 0.5rem;
            /* border-bottom: 1px solid #e2e8f0; */ /* border-slate-200 - removed for cleaner look inside card */
        }
        details > summary::-webkit-details-marker {
            display: none; /* Remove default marker in WebKit */
        }
        details > summary::before {
            content: '▶ '; /* Collapsed state indicator */
            font-size: 0.8em;
            margin-right: 0.25rem;
        }
        details[open] > summary::before {
            content: '▼ '; /* Expanded state indicator */
        }
        .settings-content {
            padding-top: 0.5rem;
            border-top: 1px solid #e2e8f0; /* Add border top to content for separation */
            margin-top: 0.5rem;
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
        <div id="swStatus" class="text-xs text-slate-500 mt-1">Initializing Service Worker...</div>
    </div>

    <div class="bookmarks-container bg-white p-6 sm:p-8 rounded-xl shadow-2xl w-full max-w-xl text-center mb-5">
        <h2 class="text-xl sm:text-2xl font-semibold mb-5 text-slate-700 text-left">Bookmarks <span class="text-sm font-normal text-slate-500">(Sorted by Visits)</span></h2>
        <ul id="bookmarksList" class="list-none p-0 m-0 text-left">
            </ul>
    </div>

    <div class="actions-container bg-white p-6 sm:p-8 rounded-xl shadow-2xl w-full max-w-xl text-left"> 
        <details>
            <summary class="text-lg font-semibold text-slate-700">Advanced Settings & Actions</summary>
            <div class="settings-content mt-4 grid grid-cols-1 md:grid-cols-2 gap-6">
                <div>
                    <h3 class="text-md font-semibold text-slate-700 mb-3">Site Permissions</h3>
                    <div class="checkbox-label">
                        <input type="checkbox" id="enableJsCheckbox" class="form-checkbox text-indigo-600">
                        <label for="enableJsCheckbox">Enable JavaScript</label>
                    </div>
                    <div class="checkbox-label">
                        <input type="checkbox" id="allowCookiesCheckbox" class="form-checkbox text-indigo-600">
                        <label for="allowCookiesCheckbox">Allow Cookies</label>
                    </div>
                </div>
                <div class="md:pl-4">
                     <h3 class="text-md font-semibold text-slate-700 mb-3">Proxy Data</h3>
                    <button id="clearDataButton"
                            class="bg-amber-400 hover:bg-amber-500 text-slate-800 font-semibold py-2 px-4 rounded-lg shadow-md w-full focus:outline-none focus:ring-2 focus:ring-offset-1 focus:ring-amber-500 transition-colors duration-150 mb-2">
                        Clear Proxy Data
                    </button>
                    <p class="text-xs text-slate-500">
                        Clears temporary proxy data (caches, session storage, non-HttpOnly cookies for this proxy). Bookmarks &amp; preferences are kept.
                    </p>
                </div>
            </div>
            <div id="messageBox" class="text-sm text-red-600 min-h-[1.25em] mt-4"></div>
        </details>
    </div>

    <script nonce="{{NONCE_MAIN_PAGE}}">
        const urlInput = document.getElementById('urlInput');
        const visitButton = document.getElementById('visitButton');
        const bookmarksList = document.getElementById('bookmarksList');
        const messageBox = document.getElementById('messageBox');
        const swStatus = document.getElementById('swStatus');
        const clearDataButton = document.getElementById('clearDataButton');
        const enableJsCheckbox = document.getElementById('enableJsCheckbox');
        const allowCookiesCheckbox = document.getElementById('allowCookiesCheckbox');

        const BOOKMARKS_LS_KEY = 'swProxyBookmarks_v4'; 
        const JS_ENABLED_COOKIE_NAME = 'proxy-js-enabled';
        const COOKIES_ENABLED_COOKIE_NAME = 'proxy-cookies-enabled';
        const PROXY_LAST_BASE_URL_COOKIE_NAME = 'proxy-last-base-url';


        function getCookie(name) {
            const nameEQ = name + "=";
            const ca = document.cookie.split(';');
            for(let i = 0; i < ca.length; i++) {
                let c = ca[i];
                while (c.charAt(0) === ' ') c = c.substring(1, c.length);
                if (c.indexOf(nameEQ) === 0) return c.substring(nameEQ.length, c.length);
            }
            return null;
        }

        function setCookie(name, value, days) {
            let expires = "";
            if (days) {
                const date = new Date();
                date.setTime(date.getTime() + (days*24*60*60*1000));
                expires = "; expires=" + date.toUTCString();
            }
            document.cookie = name + "=" + (value || "")  + expires + "; path=/; SameSite=Lax";
        }
        
        const jsEnabledCookie = getCookie(JS_ENABLED_COOKIE_NAME);
        enableJsCheckbox.checked = (jsEnabledCookie === 'true'); 
        
        const cookiesEnabledCookie = getCookie(COOKIES_ENABLED_COOKIE_NAME);
        allowCookiesCheckbox.checked = (cookiesEnabledCookie === 'true');


        function updateGlobalSettingsFromCheckboxes() {
            setCookie(JS_ENABLED_COOKIE_NAME, enableJsCheckbox.checked.toString(), 30); 
            setCookie(COOKIES_ENABLED_COOKIE_NAME, allowCookiesCheckbox.checked.toString(), 30);
        }
        
        updateGlobalSettingsFromCheckboxes();


        enableJsCheckbox.addEventListener('change', function() {
            updateGlobalSettingsFromCheckboxes();
            messageBox.textContent = 'JavaScript preference updated.';
            setTimeout(() => messageBox.textContent = '', 3500);
            const currentUrlInInput = urlInput.value.trim();
            if (currentUrlInInput) {
                 let fullUrl = currentUrlInInput;
                 if (!fullUrl.startsWith('http://') && !fullUrl.startsWith('https://')) {
                    fullUrl = 'https://' + fullUrl;
                 }
                 updateBookmarkSettings(fullUrl, this.checked, allowCookiesCheckbox.checked);
            }
        });
        
        allowCookiesCheckbox.addEventListener('change', function() {
            updateGlobalSettingsFromCheckboxes();
            messageBox.textContent = 'Cookie preference updated.';
            setTimeout(() => messageBox.textContent = '', 3500);
            const currentUrlInInput = urlInput.value.trim();
            if (currentUrlInInput) {
                 let fullUrl = currentUrlInInput;
                 if (!fullUrl.startsWith('http://') && !fullUrl.startsWith('https://')) {
                    fullUrl = 'https://' + fullUrl;
                 }
                 updateBookmarkSettings(fullUrl, enableJsCheckbox.checked, this.checked);
            }
        });


        function getBookmarks() {
            const bookmarksJson = localStorage.getItem(BOOKMARKS_LS_KEY);
            let bookmarks = bookmarksJson ? JSON.parse(bookmarksJson) : [];
            return bookmarks.map(bm => ({
                name: bm.name || bm.url, 
                url: bm.url,
                visitedCount: bm.visitedCount || 0,
                jsEnabled: typeof bm.jsEnabled === 'boolean' ? bm.jsEnabled : false, 
                cookiesEnabled: typeof bm.cookiesEnabled === 'boolean' ? bm.cookiesEnabled : false 
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
                    <span class="bookmark-name font-medium block hover:underline">\${bookmark.name} (\${bookmark.jsEnabled ? 'JS ✓' : 'JS ✗'}, \${bookmark.cookiesEnabled ? 'Cookies ✓' : 'Cookies ✗'})</span>
                    <span class="bookmark-url text-xs text-slate-500 block">\${bookmark.url}</span>
                \`;
                linkContent.addEventListener('click', () => {
                    urlInput.value = bookmark.url;
                    enableJsCheckbox.checked = bookmark.jsEnabled; 
                    allowCookiesCheckbox.checked = bookmark.cookiesEnabled;
                    updateGlobalSettingsFromCheckboxes(); 
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
        
        function updateBookmarkSettings(urlToUpdate, jsEnabledStatus, cookiesEnabledStatus) {
            let bookmarks = getBookmarks();
            const bookmarkIndex = bookmarks.findIndex(bm => bm.url === urlToUpdate);
            if (bookmarkIndex > -1) {
                bookmarks[bookmarkIndex].jsEnabled = jsEnabledStatus;
                bookmarks[bookmarkIndex].cookiesEnabled = cookiesEnabledStatus;
                saveBookmarks(bookmarks);
                displayBookmarks(); 
            }
        }


        function addOrUpdateBookmark(urlToVisit, name) {
            let bookmarks = getBookmarks();
            const existingBookmarkIndex = bookmarks.findIndex(bm => bm.url === urlToVisit);
            const currentJsEnabledSetting = enableJsCheckbox.checked; 
            const currentCookiesEnabledSetting = allowCookiesCheckbox.checked;

            if (existingBookmarkIndex > -1) {
                bookmarks[existingBookmarkIndex].visitedCount += 1;
                bookmarks[existingBookmarkIndex].jsEnabled = currentJsEnabledSetting; 
                bookmarks[existingBookmarkIndex].cookiesEnabled = currentCookiesEnabledSetting;
                if (name && name !== bookmarks[existingBookmarkIndex].name) { 
                    bookmarks[existingBookmarkIndex].name = name;
                }
            } else {
                let bookmarkName = name;
                if (!bookmarkName) {
                    try { bookmarkName = new URL(urlToVisit).hostname; } 
                    catch (e) { bookmarkName = urlToVisit; }
                }
                bookmarks.push({ 
                    name: bookmarkName, 
                    url: urlToVisit, 
                    visitedCount: 1, 
                    jsEnabled: currentJsEnabledSetting,
                    cookiesEnabled: currentCookiesEnabledSetting
                });
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
            console.log('Attempting to clear proxy data...');
            try {
                let bookmarksToKeep = localStorage.getItem(BOOKMARKS_LS_KEY);
                let jsEnabledCookieVal = getCookie(JS_ENABLED_COOKIE_NAME);
                let cookiesEnabledCookieVal = getCookie(COOKIES_ENABLED_COOKIE_NAME);


                localStorage.clear(); 
                if (bookmarksToKeep) {
                    localStorage.setItem(BOOKMARKS_LS_KEY, bookmarksToKeep); 
                }
                 if (jsEnabledCookieVal !== null) { 
                    setCookie(JS_ENABLED_COOKIE_NAME, jsEnabledCookieVal, 30);
                }
                if (cookiesEnabledCookieVal !== null) {
                    setCookie(COOKIES_ENABLED_COOKIE_NAME, cookiesEnabledCookieVal, 30);
                }
                console.log('LocalStorage (excluding bookmarks & preferences) cleared.');
                displayBookmarks(); 
                sessionStorage.clear();
                console.log('SessionStorage cleared.');
                
                console.log('Attempting to clear client-side accessible cookies for this proxy domain...');
                const cookies = document.cookie.split(";");
                for (let i = 0; i < cookies.length; i++) {
                    const cookie = cookies[i];
                    const eqPos = cookie.indexOf("=");
                    const name = eqPos > -1 ? cookie.substr(0, eqPos).trim() : cookie.trim();
                    if (name !== JS_ENABLED_COOKIE_NAME && name !== COOKIES_ENABLED_COOKIE_NAME && name !== BOOKMARKS_LS_KEY && name !== PROXY_LAST_BASE_URL_COOKIE_NAME) { 
                        document.cookie = name + "=;expires=Thu, 01 Jan 1970 00:00:00 GMT;path=/;domain=" + window.location.hostname;
                        document.cookie = name + "=;expires=Thu, 01 Jan 1970 00:00:00 GMT;path=/"; 
                    }
                }
                
                if (window.indexedDB && typeof window.indexedDB.databases === 'function') {
                    const dbs = await window.indexedDB.databases();
                    for (const db of dbs) {
                        if (db.name) {
                           try {
                                await new Promise((resolve, reject) => {
                                    const deleteRequest = window.indexedDB.deleteDatabase(db.name);
                                    deleteRequest.onsuccess = () => {
                                        console.log(\`IndexedDB: \${db.name} deleted successfully.\`);
                                        resolve();
                                    };
                                    deleteRequest.onerror = (event) => {
                                        console.error(\`IndexedDB: Error deleting \${db.name}:\`, event.target.error);
                                        reject(event.target.error);
                                    };
                                    deleteRequest.onblocked = () => {
                                        console.warn(\`IndexedDB: Deletion of \${db.name} blocked. Close other tabs/connections.\`);
                                        reject(new Error('IndexedDB deletion blocked'));
                                    };
                                });
                            } catch (e) {
                                console.error(\`IndexedDB: Failed to initiate deletion for \${db.name}\`, e);
                            }
                        }
                    }
                    console.log('IndexedDB clearing process initiated for all databases on this origin.');
                } else {
                    console.log('IndexedDB databases API not available or no databases found to clear.');
                }

                if ('serviceWorker' in navigator && window.caches) {
                    const cacheNames = await window.caches.keys();
                    for (const cacheName of cacheNames) {
                        await window.caches.delete(cacheName);
                        console.log('Cache deleted:', cacheName);
                    }
                    console.log('Service Worker caches cleared.');
                }
                console.log('Proxy data cleared. Bookmarks & preferences preserved.');
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
                updateGlobalSettingsFromCheckboxes(); 
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
  }

  rewriteUrl(originalUrlValue) {
    if (!originalUrlValue || originalUrlValue.startsWith('data:') || originalUrlValue.startsWith('blob:') || originalUrlValue.startsWith('#') || originalUrlValue.startsWith('javascript:')) {
      return originalUrlValue; 
    }
    try {
      const absoluteTargetUrl = new URL(originalUrlValue, this.targetUrl.href).toString();
      return `${this.workerUrl}/proxy?url=${encodeURIComponent(absoluteTargetUrl)}`;
    } catch (e) {
      // console.error(`Error rewriting URL value "${originalUrlValue}" against base "${this.targetUrl.href}": ${e.message}`);
      return originalUrlValue; 
    }
  }

  element(element) {
    const tagNameLower = element.tagName.toLowerCase();
    let attributesToProcess = [];

    switch (tagNameLower) {
        case 'a':
            attributesToProcess.push('href');
            // If the <a> tag has target="_blank", change it to target="_self"
            if (element.hasAttribute('target')) {
                const currentTarget = element.getAttribute('target');
                if (currentTarget && currentTarget.toLowerCase() === '_blank') {
                    element.setAttribute('target', '_self');
                }
            }
            break;
        case 'link': 
            attributesToProcess.push('href');
            break;
        case 'img':
            attributesToProcess.push('src', 'srcset');
            break;
        case 'script':
        case 'iframe':
        case 'audio':
        case 'video':
        case 'source': 
        case 'track':  
            attributesToProcess.push('src');
            break;
        case 'form':
            attributesToProcess.push('action');
            break;
        case 'meta': 
            if ((element.getAttribute('http-equiv') || '').toLowerCase() === 'refresh') {
                const content = element.getAttribute('content');
                if (content && content.toLowerCase().includes('url=')) {
                    // console.log(`Removing meta refresh tag: <meta http-equiv="refresh" content="${content}">`);
                    element.remove();
                }
            }
            return; 
    }

    for (const attrName of attributesToProcess) {
        if (tagNameLower === 'link' && attrName === 'href') {
            const rel = (element.getAttribute('rel') || '').toLowerCase();
            if (!(rel === 'stylesheet' || rel.includes('icon') || rel.includes('apple-touch-icon') || rel === 'preload' || rel === 'prefetch' || rel === 'manifest')) {
                continue; 
            }
        }
        
        const originalValue = element.getAttribute(attrName);
        if (originalValue) {
            if (attrName === 'srcset' && tagNameLower === 'img') {
                const rewrittenCandidates = originalValue
                    .split(',')
                    .map(candidate => {
                        const trimmedCandidate = candidate.trim();
                        if (!trimmedCandidate) return ''; 
                        const parts = trimmedCandidate.split(/\s+/);
                        const urlPart = parts[0];
                        const descriptor = parts.slice(1).join(' ');
                        const rewrittenUrlPart = this.rewriteUrl(urlPart);
                        return descriptor ? `${rewrittenUrlPart} ${descriptor}` : rewrittenUrlPart;
                    })
                    .filter(candidate => candidate) 
                    .join(', ');
                
                if (rewrittenCandidates !== originalValue) {
                    element.setAttribute(attrName, rewrittenCandidates);
                }
            } else {
                const rewrittenValue = this.rewriteUrl(originalValue);
                if (rewrittenValue !== originalValue) {
                    element.setAttribute(attrName, rewrittenValue);
                }
            }
        }
    }
  }
}

class ScriptInjector {
  constructor(scriptToInject, nonce) { 
    this.rawScriptContent = scriptToInject;
    this.nonce = nonce;
  }
  element(element) {
    // Construct the script tag with a nonce if provided
    const scriptTag = this.nonce 
        ? `<script nonce="${this.nonce}">${this.rawScriptContent}</script>` 
        : `<script>${this.rawScriptContent}</script>`;
    element.append(scriptTag, { html: true });
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
  const PROXY_LAST_BASE_URL_COOKIE_NAME = 'proxy-last-base-url'; 
  const JS_ENABLED_COOKIE_NAME = 'proxy-js-enabled';
  const COOKIES_ENABLED_COOKIE_NAME = 'proxy-cookies-enabled';


  // Generate a nonce for this request
  const nonce = crypto.randomUUID().replace(/-/g, '');


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

      // Check if cookies are allowed for this request based on the proxy-cookies-enabled cookie
      let cookiesAllowedForSite = true; // Default to true
      const incomingCookieHeader = request.headers.get('Cookie');
      if (incomingCookieHeader) {
          const cookies = incomingCookieHeader.split(';');
          for (let cookie of cookies) {
              cookie = cookie.trim();
              if (cookie.startsWith(COOKIES_ENABLED_COOKIE_NAME + '=')) {
                  cookiesAllowedForSite = cookie.substring(COOKIES_ENABLED_COOKIE_NAME.length + 1) === 'true';
                  break;
              }
          }
      }
      
      if (cookiesAllowedForSite) {
        const setCookieHeaders = newResponseHeaders.getAll('Set-Cookie');
        newResponseHeaders.delete('Set-Cookie'); 
        for (const cookieHeader of setCookieHeaders) {
          let parts = cookieHeader.split(';').map(part => part.trim());
          // Keep only name=value part
          const nameValuePart = parts[0]; 
          parts = [nameValuePart]; // Start with just name=value

          parts.push('Max-Age=300'); // Set Max-Age to 5 minutes
          parts.push('Path=/');      // Ensure Path is /
          if (workerUrl.startsWith('https:')) { // Add Secure if proxy is HTTPS
            parts.push('Secure');
          }
          parts.push('SameSite=Lax'); // Sensible default for SameSite
          
          newResponseHeaders.append('Set-Cookie', parts.join('; '));
        }
      } else {
        // If cookies are not allowed, remove any Set-Cookie headers from the target's response
        newResponseHeaders.delete('Set-Cookie');
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
      
      let jsEnabled = true; 
      if (incomingCookieHeader) {
          const cookies = incomingCookieHeader.split(';');
          for (let cookie of cookies) {
              cookie = cookie.trim();
              if (cookie.startsWith(JS_ENABLED_COOKIE_NAME + '=')) {
                  jsEnabled = cookie.substring(JS_ENABLED_COOKIE_NAME.length + 1) === 'true';
                  break;
              }
          }
      }

      let scriptSrcDirective;
      let currentNonceForInjectedScript = null;

      if (jsEnabled) {
          scriptSrcDirective = `* data: blob: 'unsafe-inline' 'unsafe-eval'`; 
      } else {
          scriptSrcDirective = `'nonce-${nonce}'`; 
          currentNonceForInjectedScript = nonce; 
      }

      const cspPolicy = `default-src * data: blob: 'unsafe-inline' 'unsafe-eval'; script-src ${scriptSrcDirective}; form-action 'self'; frame-src 'none'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';`;
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
            .on('a, img, script, link, form, iframe, audio, video, source, track, meta', attributeRewriterInstance)
            .on('body', new ScriptInjector(HTML_PAGE_PROXIED_CONTENT_SCRIPT, currentNonceForInjectedScript)); 
        
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
        `script-src 'self' 'nonce-${nonce}' 'unsafe-inline'; ` + 
        "style-src 'self' 'unsafe-inline'; " +  
        "font-src 'self' data:;" 
    );
    const finalHtmlPageInputForm = HTML_PAGE_INPUT_FORM.replace('{{NONCE_MAIN_PAGE}}', nonce);
    return new Response(finalHtmlPageInputForm, { headers: landingPageHeaders });
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
    const JS_ENABLED_COOKIE_NAME = 'proxy-js-enabled';
    const COOKIES_ENABLED_COOKIE_NAME = 'proxy-cookies-enabled';


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
    
    // Check if cookies are allowed for this request based on the proxy-cookies-enabled cookie
    let cookiesAllowedForSiteRequest = true; // Default to true
    const originalCookieHeader = incomingHeaders.get('cookie');
    if (originalCookieHeader) {
        const cookies = originalCookieHeader.split('; ');
        for (let cookie of cookies) {
            cookie = cookie.trim();
            if (cookie.startsWith(COOKIES_ENABLED_COOKIE_NAME + '=')) {
                cookiesAllowedForSiteRequest = cookie.substring(COOKIES_ENABLED_COOKIE_NAME.length + 1) === 'true';
                break;
            }
        }

        if (cookiesAllowedForSiteRequest) {
            const filteredCookies = cookies.filter(cookie => {
                const cookieName = cookie.split('=')[0].trim(); 
                return !cookieName.toLowerCase().startsWith('cf_') && 
                       cookieName !== PROXY_LAST_BASE_URL_COOKIE_NAME &&
                       cookieName !== JS_ENABLED_COOKIE_NAME &&
                       cookieName !== COOKIES_ENABLED_COOKIE_NAME; 
            });
            if (filteredCookies.length > 0) {
                newHeaders.set('cookie', filteredCookies.join('; '));
            }
        } // If cookiesAllowedForSiteRequest is false, no cookie header is set for the target
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

    if (incomingHeaders.has('User-Agent')) {
        newHeaders.set('User-Agent', incomingHeaders.get('User-Agent'));
    } else {
        newHeaders.set('User-Agent', 'Cloudflare-Worker-ServiceWorker-Proxy/1.3.8'); 
    }
    
    const headersToRemove = ['cf-connecting-ip', 'cf-ipcountry', 'cf-ray', 'cf-visitor', 'x-forwarded-for', 'x-forwarded-proto'];
    for(const header of headersToRemove){
        if(newHeaders.has(header)){
            newHeaders.delete(header);
        }
    }
    
    return newHeaders;
}
