// worker.js - Cloudflare Worker Script

// Content for the client-side Service Worker (sw.js)
// This will be served by the Cloudflare worker at the /sw.js path
const SERVICE_WORKER_JS = `
// sw.js - Client-side Service Worker

const PROXY_ENDPOINT = '/proxy?url='; // The endpoint in our Cloudflare worker
const SW_VERSION = '1.2.1'; // Updated version for clarity (icon change)

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
      homeLink.style.bottom = '15px'; // Adjusted for better spacing
      homeLink.style.left = '15px';  // Adjusted for better spacing
      homeLink.style.zIndex = '2147483647'; // Max z-index
      homeLink.style.backgroundColor = 'rgba(0, 123, 255, 0.7)'; // Semi-transparent blue
      homeLink.style.width = '48px'; // Fixed size for circular button
      homeLink.style.height = '48px';
      homeLink.style.display = 'flex';
      homeLink.style.alignItems = 'center';
      homeLink.style.justifyContent = 'center';
      homeLink.style.textDecoration = 'none';
      homeLink.style.borderRadius = '50%'; // Circular shape
      homeLink.style.boxShadow = '0 4px 8px rgba(0,0,0,0.3)';
      homeLink.style.transition = 'background-color 0.2s ease-in-out, transform 0.1s ease-in-out';
      
      homeLink.addEventListener('mouseover', () => {
        homeLink.style.backgroundColor = 'rgba(0, 105, 217, 0.9)'; // Darker on hover
      });
      homeLink.addEventListener('mouseout', () => {
        homeLink.style.backgroundColor = 'rgba(0, 123, 255, 0.7)';
      });
       homeLink.addEventListener('mousedown', () => homeLink.style.transform = 'scale(0.95)');
       homeLink.addEventListener('mouseup', () => homeLink.style.transform = 'scale(1)');
      
      // Ensure body exists before appending
      if (document.body) {
        document.body.appendChild(homeLink);
      } else {
        // If body is not ready yet, wait for DOMContentLoaded
        window.addEventListener('DOMContentLoaded', () => {
          if (document.body) {
            document.body.appendChild(homeLink);
          } else {
            console.error("Proxy Home Link: document.body not found even after DOMContentLoaded.");
          }
        });
      }
    }

    addProxyHomeLink(); // Call the function to add the link

    document.addEventListener('click', function(event) {
      // Find the closest <a> element to the click target
      let anchorElement = event.target.closest('a');

      if (anchorElement) {
        // If the clicked link is our "Proxy Home" link, allow default navigation.
        // The browser will handle its href="/".
        if (anchorElement.id === 'proxy-home-link') {
          console.log('Proxy Home link clicked, navigating to /');
          // No event.preventDefault() here, let the browser navigate.
          return; 
        }

        const href = anchorElement.getAttribute('href');
        
        // Process other links to go through the proxy and open in the current tab
        if (href && !href.startsWith('javascript:') && !href.startsWith('#')) {
          event.preventDefault(); // Prevent default navigation (including new tab behavior)

          const originalPageBase = getOriginalPageBaseUrl();
          if (!originalPageBase) {
            console.error("Proxy Click Handler: Could not determine original page base URL for link:", href);
            // Fallback: attempt to proxy with href as is, in current tab
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            console.warn('Proxy Click Handler: Original page base URL missing. Attempting to proxy href directly:', newProxyNavUrl);
            window.location.href = newProxyNavUrl;
            return;
          }

          try {
            // Resolve the clicked href against the original page's base URL
            const absoluteTargetUrl = new URL(href, originalPageBase).toString();
            
            // Construct the new proxy URL for navigation
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(absoluteTargetUrl);
            
            console.log('Proxy Click Handler: Navigating to proxied URL (current tab):', newProxyNavUrl);
            window.location.href = newProxyNavUrl; // Navigate in the current tab
          } catch (e) {
            console.error("Proxy Click Handler: Error resolving or navigating link:", href, e);
            // Fallback: attempt to navigate via proxy with href as is, in current tab
            const fallbackAbsoluteTargetUrl = href;
            const newProxyNavUrl = window.location.origin + '/proxy?url=' + encodeURIComponent(fallbackAbsoluteTargetUrl);
            console.warn('Proxy Click Handler: Error during URL resolution. Attempting to proxy href directly:', newProxyNavUrl);
            window.location.href = newProxyNavUrl;
          }
        }
      }
    }, true); // Use capture phase to catch clicks early

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
    <title>Web Proxy (Service Worker Edition)</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
            background: #f0f2f5; display: flex; align-items: center; justify-content: center;
            min-height: 100vh; margin: 0; padding: 16px; box-sizing: border-box;
        }
        .container {
            background-color: #ffffff; padding: 32px; border-radius: 12px;
            box-shadow: 0 10px 25px rgba(0, 0, 0, 0.1); width: 100%;
            max-width: 500px; text-align: center;
        }
        h1 { font-size: 28px; font-weight: 700; margin-bottom: 32px; color: #333333; }
        label {
            display: block; font-size: 14px; font-weight: 500; color: #555555;
            margin-bottom: 8px; text-align: left;
        }
        input[type="text"] {
            width: calc(100% - 32px); padding: 12px 16px; border: 1px solid #dddddd;
            border-radius: 8px; box-shadow: inset 0 1px 2px rgba(0,0,0,0.05);
            font-size: 16px; margin-bottom: 24px; box-sizing: border-box;
        }
        input[type="text"]:focus {
            outline: none; border-color: #007bff;
            box-shadow: 0 0 0 3px rgba(0, 123, 255, 0.25);
        }
        button {
            width: 100%; background-color: #007bff; color: white; font-weight: 600;
            padding: 12px 16px; border: none; border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1); cursor: pointer; font-size: 16px;
            transition: background-color 0.2s ease-in-out, transform 0.1s ease-in-out;
        }
        button:hover { background-color: #0056b3; }
        button:active { transform: translateY(1px); background-color: #004085; }
        .message-box { margin-top: 24px; font-size: 14px; color: #dc3545; min-height: 1.25em; }
        .sw-status { margin-top: 16px; font-size: 12px; color: #666; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Secure Web Proxy</h1>
        <div>
            <label for="urlInput">Enter URL to visit:</label>
            <input type="text" id="urlInput" placeholder="e.g., https://example.com">
        </div>
        <button id="visitButton"> Visit Securely </button>
        <div id="messageBox" class="message-box"></div>
        <div id="swStatus" class="sw-status">Initializing Service Worker...</div>
    </div>
    <script>
        // Script for the main input form page
        const urlInput = document.getElementById('urlInput');
        const visitButton = document.getElementById('visitButton');
        const messageBox = document.getElementById('messageBox');
        const swStatus = document.getElementById('swStatus');

        if ('serviceWorker' in navigator) {
            window.addEventListener('load', () => {
                navigator.serviceWorker.register('/sw.js', { scope: '/' })
                    .then(registration => {
                        swStatus.textContent = 'Service Worker registered successfully.';
                        if (navigator.serviceWorker.controller) {
                            swStatus.textContent += ' (Active and controlling)';
                        } else {
                             swStatus.textContent += ' (Registered, will control after next load/activation)';
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
            if (!destUrl.startsWith('http://') && !destUrl.startsWith('https://')) {
                if (destUrl.includes('.') && !destUrl.includes(' ') && !destUrl.startsWith('/')) {
                    destUrl = 'https://' + destUrl; urlInput.value = destUrl;
                } else { messageBox.textContent = 'Invalid URL. Include http(s):// or valid domain.'; return; }
            }
            try { new URL(destUrl); } catch (e) { messageBox.textContent = 'Invalid URL format.'; return; }
            window.location.href = window.location.origin + '/proxy?url=' + encodeURIComponent(destUrl);
        });
        urlInput.addEventListener('keypress', e => { if (e.key === 'Enter') { e.preventDefault(); visitButton.click(); }});
    </script></body></html>`;

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
        headers: filterRequestHeaders(request.headers, targetUrlObj.hostname, workerUrl),
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
 */
function filterRequestHeaders(incomingHeaders, targetHostname, workerUrl) {
    const newHeaders = new Headers();
    let targetOrigin = "http://" + targetHostname; 
    try {
        targetOrigin = new URL("https://" + targetHostname).origin; 
    } catch(e) {
        try { targetOrigin = new URL(targetHostname).origin; } 
        catch(e) { /* keep default */ }
    }
    const defaultReferer = targetOrigin + "/";

    const headersToForward = [
        'Accept', 'Accept-Charset', 'Accept-Encoding', 'Accept-Language',
        'User-Agent', 'Content-Type', 'Authorization', 'Range', 'X-Requested-With'
    ];

    for (const headerName of headersToForward) {
        if (incomingHeaders.has(headerName)) {
            newHeaders.set(headerName, incomingHeaders.get(headerName));
        }
    }
    
    if (incomingHeaders.has('cookie')) {
        newHeaders.set('cookie', incomingHeaders.get('cookie'));
    }
    
    const incomingReferer = incomingHeaders.get('Referer');
    if (incomingReferer && !incomingReferer.startsWith(workerUrl)) { 
        newHeaders.set('Referer', incomingReferer);
    } else {
        newHeaders.set('Referer', defaultReferer);
    }

    if (!newHeaders.has('User-Agent')) {
        newHeaders.set('User-Agent', 'Cloudflare-Worker-ServiceWorker-Proxy/1.2'); 
    }
    
    for (let key of newHeaders.keys()) { 
        if (key.toLowerCase().startsWith('cf-')) {
            newHeaders.delete(key);
        }
    }
    newHeaders.delete('X-Forwarded-For');
    newHeaders.delete('X-Forwarded-Proto');
    
    return newHeaders;
}
