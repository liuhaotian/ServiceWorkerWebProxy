package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdhtml "html" // Standard library html, aliased to avoid conflict
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html" // For HTML parsing and rewriting
)

// Configuration
var (
	listenPort string
	// Default privacy settings
	defaultGlobalJSEnabled      = false
	defaultGlobalCookiesEnabled = false
	defaultGlobalIframesEnabled = false
	defaultGlobalRawModeEnabled = false 

	authServiceURL string
)

// Cookie names & Constants
const (
	authCookieName   = "CF_Authorization" 
	maxRedirects     = 5                  
	proxyRequestPath = "/proxy"
	serviceWorkerPath = "/sw.js" 
	fallbackNonce    = "ZmFsbGJhY2tOb25jZQ==" 
)

// Regex for parsing forms (used in auth flow)
var (
	formActionRegex    = regexp.MustCompile(`(?is)<form[^>]*action\s*=\s*["']([^"']+)["'][^>]*>`)
	hiddenInputRegex   = regexp.MustCompile(`(?is)<input[^>]*type\s*=\s*["']hidden["'][^>]*name\s*=\s*["']([^"']+)["'][^>]*value\s*=\s*["']([^"']*)["'][^>]*>`)
	nonceInputRegex    = regexp.MustCompile(`(?is)<input[^>]*name\s*=\s*["']nonce["'][^>]*value\s*=\s*["']([^"']+)["']`)
	codeInputFormRegex = regexp.MustCompile(`(?is)<form[^>]*action\s*=\s*["']([^"']*/cdn-cgi/access/callback[^"']*)["'][^>]*>`) 
	cssURLRegex        = regexp.MustCompile(`(?i)url\s*\(\s*(?:'([^']*)'|"([^"]*)"|([^)\s'"]+))\s*\)`)
)

// sitePreferences holds the privacy settings for a site.
type sitePreferences struct {
	JavaScriptEnabled    bool
	CookiesEnabled       bool
	IframesEnabled       bool
	RawModeEnabled       bool 
}

// JWTHeader represents the decoded header of a JWT
type JWTHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

// JWTPayload represents common claims in a JWT payload
type JWTPayload struct {
	Email         string      `json:"email"`
	IdentityNonce string      `json:"identity_nonce"`
	Issuer        string      `json:"iss"`
	Audience      interface{} `json:"aud"` // Can be string or []string
	ExpiresAt     int64       `json:"exp"`
	IssuedAt      int64       `json:"iat"`
	NotBefore     int64       `json:"nbf"`
	Subject       string      `json:"sub"`
	Type          string      `json:"type"`
	Country       string      `json:"country"`
}

// --- Embedded Static Assets ---

const styleCSSContent = `
body { 
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
    line-height: 1.6;
}
#bookmarks-list .bookmark-item:last-child {
    border-bottom: none;
}
.bookmark-prefs-emojis span { 
    cursor: default; 
}
details > summary {
  list-style-type: disclosure-open; 
}
details[open] > summary {
  list-style-type: disclosure-open; 
}
.favicon-fallback {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2.5rem; 
    height: 2.5rem; 
    background-color: #e5e7eb; 
    border-radius: 0.125rem; 
    color: #9ca3af; 
    font-size: 0.75rem; 
    font-weight: 500;
}
#global-settings-indicators span {
    cursor: default; 
		padding: 0.25rem 0.35rem; 
    border-radius: 0.25rem;
		font-size: 1rem; 
}
`

// makeInjectedHTML generates the HTML for the proxy home button and an injected script
// that includes GET form interception logic.
// scriptNonce is used for the script tag's nonce attribute.
func makeInjectedHTML(scriptNonce string) string {
	var sb strings.Builder
	sb.WriteString(`<style id="proxy-home-button-styles" type="text/css">
#proxy-home-button {
    position: fixed !important;
    bottom: 20px !important;
    right: 20px !important;
    width: 48px !important; 
    height: 48px !important; 
    padding: 0 !important; 
    background-color: rgba(37, 99, 235, 0.9) !important; 
    color: white !important;
    text-decoration: none !important;
    border-radius: 50% !important; 
    box-shadow: 0 4px 8px rgba(0,0,0,0.2) !important;
    z-index: 2147483647 !important;
    border: none !important; 
    cursor: pointer !important; 
    display: inline-flex !important; 
    align-items: center !important;
    justify-content: center !important;
    transition: background-color 0.2s ease-in-out, transform 0.2s ease-in-out !important;
}
#proxy-home-button:hover {
    background-color: rgba(29, 78, 216, 1) !important; 
    transform: scale(1.1) !important; 
}
#proxy-home-button svg {
    width: 24px !important; 
    height: 24px !important;
    fill: currentColor !important;
}
</style>
<a href="/" id="proxy-home-button" title="Return to Proxy Home">
    <svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z"/></svg>
</a>`)

	sb.WriteString(`<script nonce="`)
	sb.WriteString(stdhtml.EscapeString(scriptNonce))
	sb.WriteString(`">`)
	sb.WriteString(`
(function() {
    // Derives the original page's base URL from the current window.location (the proxy URL).
    let originalPageBaseURL = '';
    try {
        const currentProxyURL = new URL(window.location.href);
        if (currentProxyURL.pathname === '/proxy' && currentProxyURL.searchParams.has('url')) {
            originalPageBaseURL = currentProxyURL.searchParams.get('url');
        }
    } catch (e) {
        console.error('Proxy JS (injected): Error deriving originalPageBaseURL:', e);
    }
    if (!originalPageBaseURL) {
        console.warn('Proxy JS (injected): originalPageBaseURL not determined; GET form interception may be unreliable for relative actions.');
        originalPageBaseURL = window.location.href; // Fallback
    }

    // Intercepts GET form submissions to correctly construct the proxied URL.
    document.addEventListener('submit', function(event) {
        const form = event.target;
        if (form && form.method && form.method.toLowerCase() === 'get') {
            
            try {
                const formActionAttr = form.getAttribute('action') || ''; 
                const currentProxyOrigin = window.location.origin;
                const proxyPath = '/proxy'; 

                const tempActionURL = new URL(formActionAttr, window.location.href); 

                let intendedTargetActionBaseStr;

                if (tempActionURL.origin === currentProxyOrigin && 
                    tempActionURL.pathname === proxyPath && 
                    tempActionURL.searchParams.has('url')) {
                    intendedTargetActionBaseStr = tempActionURL.searchParams.get('url');
                } else {
                    const resolvedAction = new URL(formActionAttr, originalPageBaseURL);
                    intendedTargetActionBaseStr = resolvedAction.toString();
                }
                
                if (!intendedTargetActionBaseStr) {
                     console.warn('Proxy JS (injected): Could not determine intended target for GET form. Action:', formActionAttr);
                     return; 
                }
                
                event.preventDefault(); 
                console.log('Proxy JS (injected): Intercepted GET form. Target base:', intendedTargetActionBaseStr);

                const finalTargetUrl = new URL(intendedTargetActionBaseStr);
                const formData = new FormData(form);
                
                formData.forEach((value, key) => {
                    finalTargetUrl.searchParams.append(key, value);
                });

                const newProxyNavUrl = new URL(proxyPath, currentProxyOrigin); 
                newProxyNavUrl.searchParams.set('url', finalTargetUrl.toString());
                
                console.log('Proxy JS (injected): Navigating via GET form to:', newProxyNavUrl.toString());
                window.location.href = newProxyNavUrl.toString();

            } catch (e) {
                console.error('Proxy JS (injected): Error in GET form interception:', e);
            }
        }
    }, true); 
})();
`)
	sb.WriteString(`</script>`)
	return sb.String()
}

const embeddedSWContent = `
// --- Start of embeddedSWContent (Service Worker Code) ---
const PROXY_ENDPOINT = '/proxy'; 
self.addEventListener('install', event => {
    console.log('Service Worker: Installing...');
    event.waitUntil(self.skipWaiting()); 
});

self.addEventListener('activate', event => {
    console.log('Service Worker: Activating...');
    event.waitUntil(self.clients.claim()); 
});

self.addEventListener('fetch', event => {
    const request = event.request;
    const requestUrl = new URL(request.url);

    // 1. Let browser handle known internal paths or root navigation
    if (requestUrl.origin === self.origin && 
        (
            requestUrl.pathname.startsWith('/auth/') || 
            requestUrl.pathname === '/sw.js' ||
            (request.mode === 'navigate' && requestUrl.pathname === '/')
        )
    ) {
        console.log('SW: BYPASSING request (auth, self, root nav):', request.url);
        return; 
    }
    
    // 2. Let browser handle if request is already perfectly proxied
    if (requestUrl.origin === self.origin && 
        requestUrl.pathname === PROXY_ENDPOINT && 
        requestUrl.searchParams.has('url')) {
        console.log('SW: Letting browser handle (already proxied):', request.url);
        return; 
    }

    // Ignore non-http/https requests (should be rare if coming from a proxied page, but good to have)
    if (!requestUrl.protocol.startsWith('http')) {
        console.log('SW: Ignoring non-http(s) request:', request.url);
        return; 
    }
    
    // 3. Catch-all: Intercept and rewrite/rebase
    event.respondWith(async function() {
        try {
            const client = await self.clients.get(event.clientId);
            if (!client || !client.url) {
                console.log('SW: No client or client.url for interception, fetching request as is:', request.url);
                return fetch(request); 
            }

            const clientPageUrl = new URL(client.url); 

            // Ensure the page making the request is itself a proxied page hosted by this proxy.
            // Otherwise, don't interfere (e.g. requests from the landing page itself if it made an unhandled request).
            if (!(clientPageUrl.origin === self.origin && clientPageUrl.pathname === PROXY_ENDPOINT && clientPageUrl.searchParams.has('url'))) {
                console.log('SW: Client page is not a proxied page, fetching request as is:', request.url, 'Client URL:', client.url);
                return fetch(request); 
            }

            const originalProxiedBaseUrlString = clientPageUrl.searchParams.get('url');
            if (!originalProxiedBaseUrlString) {
                console.error('SW: Could not get original proxied base URL from client:', client.url);
                return fetch(request); 
            }

            let finalTargetUrlString;
            const baseForResolution = new URL(originalProxiedBaseUrlString);

            if (requestUrl.origin === self.origin) {
                // Request is to the proxy's own origin (e.g., myproxy.com/some/path or myproxy.com/proxy?q=test)
                if (requestUrl.pathname === PROXY_ENDPOINT && !requestUrl.searchParams.has('url')) {
                    // Case: myproxy.com/proxy?q=test (missing 'url' param)
                    // Apply query/hash to the original target's base URL.
                    const tempTargetUrl = new URL(baseForResolution.href); 
                    tempTargetUrl.search = requestUrl.search;
                    tempTargetUrl.hash = requestUrl.hash;
                    finalTargetUrlString = tempTargetUrl.toString();
                    console.log('SW: Resolved same-origin /proxy request (no url param):', request.url.toString(), 'to:', finalTargetUrlString);
                } else {
                    // Case: myproxy.com/some/other/path.js (e.g. un-rewritten relative path by backend)
                    // Resolve the request's path, search, and hash against the original target's base URL.
                    finalTargetUrlString = new URL(requestUrl.pathname + requestUrl.search + requestUrl.hash, baseForResolution).toString();
                    console.log('SW: Resolved same-origin relative/other request:', request.url.toString(), 'to:', finalTargetUrlString);
                }
            } else {
                // Request is to an external origin (e.g., https://some-cdn.com/script.js)
                finalTargetUrlString = request.url;
                console.log('SW: Request is to external origin, using as is:', finalTargetUrlString);
            }
            
            const newProxyRequestUrl = new URL(PROXY_ENDPOINT, self.location.origin);
            newProxyRequestUrl.searchParams.set('url', finalTargetUrlString);
            
            console.log('SW: REWRITING & FETCHING. Original: [' + request.url + '], Proxied via: [' + newProxyRequestUrl.toString() + ']');

            const newHeaders = new Headers(request.headers);
            newHeaders.delete('Range'); 
            newHeaders.delete('If-Range');

            return fetch(newProxyRequestUrl.toString(), {
                method: request.method,
                headers: newHeaders,
                body: (request.method === 'GET' || request.method === 'HEAD') ? undefined : await request.blob(),
                mode: 'cors', 
                credentials: 'include', 
                redirect: 'manual',  
            });

        } catch (error) {
            console.error('SW Fetch Error:', error, 'For request:', request.url);
            return new Response('Service Worker fetch processing error: ' + error.message, { status: 500, headers: { 'Content-Type': 'text/plain'} });
        }
    }());
});
// --- End of embeddedSWContent ---
`

const clientJSContentForEmbedding = `
// --- Start of clientJSContentForEmbedding ---
    document.addEventListener('DOMContentLoaded', () => {
        if ('serviceWorker' in navigator) {
            navigator.serviceWorker.register('/sw.js', { scope: '/' })
                .then(registration => {
                    console.log('Service Worker registered with scope:', registration.scope);
                    registration.update(); 
                })
                .catch(error => console.error('Service Worker registration failed:', error));
            
            navigator.serviceWorker.oncontrollerchange = () => {
                console.log('New Service Worker activated.');
            };
        }

        const urlInput = document.getElementById('url-input');
        const visitBtn = document.getElementById('visit-btn'); 
        const errorMessageDiv = document.getElementById('error-message');
        
        const globalJsCheckbox = document.getElementById('global-js');
        const globalCookiesCheckbox = document.getElementById('global-cookies');
        const globalIframesCheckbox = document.getElementById('global-iframes');
        const globalRawModeCheckbox = document.getElementById('global-raw-mode'); 
        const globalSettingsIndicatorsDiv = document.getElementById('global-settings-indicators');


        const bookmarksList = document.getElementById('bookmarks-list');
        const bookmarkCurrentSiteBtn = document.getElementById('bookmark-current-site-btn'); 

        const settingsKeys = { 
            js: 'proxy-js-enabled', 
            cookies: 'proxy-cookies-enabled', 
            iframes: 'proxy-iframes-enabled',
            rawMode: 'proxy-raw-mode-enabled' 
        };
        
        function updateGlobalSettingIndicators() {
            if (!globalSettingsIndicatorsDiv) return; 

            const jsEnabled = globalJsCheckbox.checked;
            const cookiesEnabled = globalCookiesCheckbox.checked;
            const iframesEnabled = globalIframesCheckbox.checked;
            const rawModeEnabled = globalRawModeCheckbox.checked; 

            let indicatorsHTML = '';
            indicatorsHTML += '<span title="JavaScript: ' + (jsEnabled ? 'Enabled' : 'Disabled') + '" class="' + (jsEnabled ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-700') + '">' + (jsEnabled ? '⚙️' : '🚫') + '</span>';
            indicatorsHTML += '<span title="Cookies: ' + (cookiesEnabled ? 'Allowed' : 'Blocked') + '" class="ml-1 ' + (cookiesEnabled ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-700') + '">' + (cookiesEnabled ? '🍪' : '🚫') + '</span>';
            indicatorsHTML += '<span title="Iframes: ' + (iframesEnabled ? 'Allowed' : 'Blocked') + '" class="ml-1 ' + (iframesEnabled ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-700') + '">' + (iframesEnabled ? '🖼️' : '🚫') + '</span>';
            indicatorsHTML += '<span title="Raw Mode: ' + (rawModeEnabled ? 'ON (No Server Rewrite)' : 'OFF (Server Rewrite Active)') + '" class="ml-1 ' + (rawModeEnabled ? 'bg-yellow-100 text-yellow-700' : 'bg-red-100 text-red-700') + '">' + (rawModeEnabled ? '🥩' : '🚫') + '</span>'; 
            
            globalSettingsIndicatorsDiv.innerHTML = indicatorsHTML;
        }

        function loadGlobalSettings() {
            globalJsCheckbox.checked = localStorage.getItem(settingsKeys.js) === 'true';
            globalCookiesCheckbox.checked = localStorage.getItem(settingsKeys.cookies) === 'true';
            globalIframesCheckbox.checked = localStorage.getItem(settingsKeys.iframes) === 'true';
            globalRawModeCheckbox.checked = localStorage.getItem(settingsKeys.rawMode) === 'true'; 
            updateGlobalPreferenceCookies(getGlobalSettings()); 
            updateGlobalSettingIndicators(); 
        }

        function getGlobalSettings() {
            return {
                js: globalJsCheckbox.checked,
                cookies: globalCookiesCheckbox.checked,
                iframes: globalIframesCheckbox.checked,
                rawMode: globalRawModeCheckbox.checked 
            };
        }

        function saveGlobalSettings() {
            const settings = getGlobalSettings();
            localStorage.setItem(settingsKeys.js, settings.js);
            localStorage.setItem(settingsKeys.cookies, settings.cookies);
            localStorage.setItem(settingsKeys.iframes, settings.iframes);
            localStorage.setItem(settingsKeys.rawMode, settings.rawMode); 
            updateGlobalPreferenceCookies(settings); 
            updateGlobalSettingIndicators(); 
        }

        function updateGlobalPreferenceCookies(prefs) { 
            const cookieOptions = 'path=/; SameSite=Lax; max-age=31536000'; 
            document.cookie = 'proxy-js-enabled=' + prefs.js + '; ' + cookieOptions; 
            document.cookie = 'proxy-cookies-enabled=' + prefs.cookies + '; ' + cookieOptions; 
            document.cookie = 'proxy-iframes-enabled=' + prefs.iframes + '; ' + cookieOptions; 
            document.cookie = 'proxy-raw-mode-enabled=' + prefs.rawMode + '; ' + cookieOptions; 
        }

        globalJsCheckbox.addEventListener('change', saveGlobalSettings);
        globalCookiesCheckbox.addEventListener('change', saveGlobalSettings);
        globalIframesCheckbox.addEventListener('change', saveGlobalSettings);
        globalRawModeCheckbox.addEventListener('change', saveGlobalSettings); 


        if (visitBtn) {
            visitBtn.addEventListener('click', (event) => {
                event.preventDefault(); 
                errorMessageDiv.style.display = 'none';
                errorMessageDiv.textContent = '';
                const targetUrlRaw = urlInput.value.trim();

                if (!targetUrlRaw) {
                    showError("URL cannot be empty.");
                    return;
                }

                let processedUrl = targetUrlRaw;
                if (!processedUrl.match(/^https?:\/\//i) && processedUrl.includes('.')) {
                    processedUrl = 'https://' + processedUrl;
                }

                if (!isValidHttpUrl(processedUrl)) {
                    showError("Please enter a valid URL (e.g., example.com or https://example.com).");
                    return;
                }

                const currentGlobalPrefs = getGlobalSettings();
                let siteName;
                try {
                    siteName = new URL(processedUrl).hostname;
                } catch (e) {
                    siteName = "Bookmarked Site"; 
                }
                incrementBookmarkVisitCount(processedUrl, siteName, currentGlobalPrefs); 
                loadBookmarks(); 

                updateGlobalPreferenceCookies(currentGlobalPrefs); 
                window.location.href = '/proxy?url=' + encodeURIComponent(processedUrl);
            });
        }

        function isValidHttpUrl(string) {
            let url;
            try {
                url = new URL(string);
            } catch (_) {
                return false;  
            }
            return url.protocol === "http:" || url.protocol === "https:";
        }

        function showError(message) {
            errorMessageDiv.textContent = message;
            errorMessageDiv.style.display = 'block';
        }

        const BOOKMARKS_LS_KEY = 'proxy-bookmarks-v5'; 

        function incrementBookmarkVisitCount(url, name, prefs) {
            const bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            const existingBookmarkIndex = bookmarks.findIndex(bm => bm.url === url);

            if (existingBookmarkIndex > -1) {
                bookmarks[existingBookmarkIndex].visitedCount = (bookmarks[existingBookmarkIndex].visitedCount || 0) + 1;
                bookmarks[existingBookmarkIndex].prefs = prefs; 
                bookmarks[existingBookmarkIndex].name = name; 
                console.log('Incremented visit count for:', url);
            } else {
                bookmarks.push({ name, url, prefs, visitedCount: 1 });
                console.log('Added new bookmark with visit count 1 for:', url);
            }
            localStorage.setItem(BOOKMARKS_LS_KEY, JSON.stringify(bookmarks));
        }
        
        function truncateUrl(url, maxLength = 45) {
            if (url.length <= maxLength) {
                return url;
            }
            try {
                const parsedUrl = new URL(url);
                let display = parsedUrl.hostname + parsedUrl.pathname;
                if (parsedUrl.search) display += parsedUrl.search;
                if (parsedUrl.hash) display += parsedUrl.hash;
                
                if (display.length <= maxLength) return display;
                return display.substring(0, maxLength - 3) + "...";

            } catch (e) { 
                 if (url.length <= maxLength) return url;
                 return url.substring(0, maxLength - 3) + "...";
            }
        }

        function loadBookmarks() {
            let bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            bookmarks.sort((a, b) => (b.visitedCount || 0) - (a.visitedCount || 0));
            bookmarksList.innerHTML = ''; 

            if (bookmarks.length === 0) {
                const p = document.createElement('p');
                p.className = 'text-gray-500 text-center py-4';
                p.textContent = 'No bookmarks yet. Enter a URL to start Browse!';
                bookmarksList.appendChild(p);
                return;
            }

            bookmarks.forEach((bm) => { 
                const item = document.createElement('div');
                item.className = 'bookmark-item flex items-start p-3 border-b border-gray-200 hover:bg-gray-50 transition-colors duration-150';
                
                let hostname = 'default';
                try {
                    hostname = new URL(bm.url).hostname;
                } catch (e) { console.warn("Could not parse hostname for icon:", bm.url); }
                
                const baseIconServiceUrl = 'https://external-content.duckduckgo.com/ip3/';
                const fullIconTargetUrl = baseIconServiceUrl + hostname + '.ico';
                const iconUrl = '/proxy?url=' + encodeURIComponent(fullIconTargetUrl);

                const displayUrl = escapeHTML(truncateUrl(bm.url));

                const iconContainer = document.createElement('div');
                iconContainer.className = 'flex-shrink-0 w-10 h-10 mr-3 mt-1';
                
                const iconImg = document.createElement('img');
                iconImg.src = iconUrl;
                iconImg.alt = ''; 
                iconImg.className = 'w-full h-full object-contain rounded-sm';
                
                const fallbackDiv = document.createElement('div');
                fallbackDiv.className = 'favicon-fallback';
                fallbackDiv.style.display = 'none';
                fallbackDiv.textContent = hostname.substring(0,1).toUpperCase();

                iconImg.onerror = () => {
                    iconImg.style.display = 'none';
                    fallbackDiv.style.display = 'flex';
                };
                
                iconContainer.appendChild(iconImg);
                iconContainer.appendChild(fallbackDiv);
                item.appendChild(iconContainer);

                const infoContainer = document.createElement('div');
                infoContainer.className = 'flex-grow overflow-hidden min-w-0';

                const firstLineDiv = document.createElement('div');
                firstLineDiv.className = 'flex justify-between items-center';

                const link = document.createElement('a');
                link.href = '#';
                link.className = 'go-bookmark-link text-blue-600 hover:text-blue-800 hover:underline font-medium truncate text-base';
                link.dataset.url = bm.url;
                link.dataset.prefs = JSON.stringify(bm.prefs);
                link.dataset.name = bm.name;
                link.title = bm.name;
                link.textContent = bm.name;
                firstLineDiv.appendChild(link);

                const visitCountSpan = document.createElement('span');
                visitCountSpan.className = 'text-sm text-gray-600 ml-2 whitespace-nowrap';
                visitCountSpan.textContent = 'Visits: ' + (bm.visitedCount || 0);
                firstLineDiv.appendChild(visitCountSpan);
                infoContainer.appendChild(firstLineDiv);

                const secondLineDiv = document.createElement('div');
                secondLineDiv.className = 'flex justify-between items-center mt-1';

                const urlSmall = document.createElement('small');
                urlSmall.className = 'text-gray-500 text-xs truncate';
                urlSmall.title = bm.url;
                urlSmall.textContent = displayUrl;
                secondLineDiv.appendChild(urlSmall);

                const emojisSpan = document.createElement('span');
                emojisSpan.className = 'bookmark-prefs-emojis text-xs ml-2 whitespace-nowrap';
                
                emojisSpan.appendChild(createEmojiSpan('JavaScript', bm.prefs.js, '⚙️', '🚫'));
                emojisSpan.appendChild(createEmojiSpan('Cookies', bm.prefs.cookies, '🍪', '🚫', 'ml-1'));
                emojisSpan.appendChild(createEmojiSpan('Iframes', bm.prefs.iframes, '🖼️', '🚫', 'ml-1'));
                emojisSpan.appendChild(createEmojiSpan('Raw Mode', bm.prefs.rawMode, '🥩', '🚫', 'ml-1')); 
                
                secondLineDiv.appendChild(emojisSpan);
                infoContainer.appendChild(secondLineDiv);
                item.appendChild(infoContainer);

                const deleteContainer = document.createElement('div');
                deleteContainer.className = 'ml-2 flex-shrink-0 self-center';
                const deleteButton = document.createElement('button');
                deleteButton.dataset.url = bm.url;
                deleteButton.className = 'delete-bookmark text-red-500 hover:text-red-700 p-1 rounded hover:bg-red-100 focus:outline-none focus:ring-2 focus:ring-red-300';
                deleteButton.title = 'Delete Bookmark';
                deleteButton.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" /></svg>';
                deleteContainer.appendChild(deleteButton);
                item.appendChild(deleteContainer);
                
                bookmarksList.appendChild(item);
            });

            document.querySelectorAll('.go-bookmark-link').forEach(link => {
                link.addEventListener('click', function(e) {
                    e.preventDefault();
                    const url = this.dataset.url;
                    const name = this.dataset.name; 
                    const bookmarkPrefs = JSON.parse(this.dataset.prefs);
                    
                    globalJsCheckbox.checked = bookmarkPrefs.js;
                    globalCookiesCheckbox.checked = bookmarkPrefs.cookies;
                    globalIframesCheckbox.checked = bookmarkPrefs.iframes;
                    globalRawModeCheckbox.checked = !!bookmarkPrefs.rawMode; 
                    saveGlobalSettings(); 

                    incrementBookmarkVisitCount(url, name, bookmarkPrefs); 
                    window.location.href = '/proxy?url=' + encodeURIComponent(url);
                });
            });
            
            document.querySelectorAll('.delete-bookmark').forEach(button => {
                button.addEventListener('click', function(e) {
                    e.stopPropagation(); 
                    if(confirm('Are you sure you want to delete this bookmark?')) {
                        const urlToDelete = this.dataset.url;
                        const allBookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
                        const originalIndex = allBookmarks.findIndex(bm => bm.url === urlToDelete);
                        if (originalIndex !== -1) {
                            deleteBookmark(originalIndex);
                        } else {
                             console.error("Could not find bookmark to delete by URL:", urlToDelete);
                             alert("Error: Could not find bookmark to delete.");
                        }
                    }
                });
            });
        }
        
        function createEmojiSpan(titlePrefix, isEnabled, enabledEmoji, disabledEmoji, additionalClasses = '') {
            const span = document.createElement('span');
            span.title = titlePrefix + ': ' + (isEnabled ? 'Enabled' : 'Disabled');
            if (titlePrefix === 'Raw Mode') { 
                 span.title = titlePrefix + ': ' + (isEnabled ? 'ON (No Server Rewrite)' : 'OFF (Server Rewrite Active)');
            }
            span.textContent = (isEnabled ? enabledEmoji : disabledEmoji) + ' ';
            if (additionalClasses) {
                span.className = additionalClasses;
            }
            return span;
        }

        function deleteBookmark(index) { 
            const bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            if (index >= 0 && index < bookmarks.length) {
                bookmarks.splice(index, 1);
                localStorage.setItem(BOOKMARKS_LS_KEY, JSON.stringify(bookmarks));
                loadBookmarks(); 
            } else {
                console.error("Invalid index for bookmark deletion:", index);
            }
        }

        function escapeHTML(str) {
            if (typeof str !== 'string') return '';
            const p = document.createElement('p');
            p.appendChild(document.createTextNode(str));
            return p.innerHTML;
        }

        if (window.location.pathname === '/proxy' && window.location.search.includes('url=')) {
            if(bookmarkCurrentSiteBtn) {
                bookmarkCurrentSiteBtn.style.display = 'inline-block'; 
                bookmarkCurrentSiteBtn.onclick = () => {
                    const currentProxiedUrlParams = new URLSearchParams(window.location.search);
                    const currentUrl = currentProxiedUrlParams.get('url');
                    if (!currentUrl) return;
                    
                    let siteName;
                    try {
                        siteName = new URL(currentUrl).hostname;
                    } catch (e) {
                        siteName = "Current Site";
                    }
                    incrementBookmarkVisitCount(currentUrl, siteName, getGlobalSettings());
                    loadBookmarks();
                    alert('Current site bookmarked/visit count updated!');
                };
            }
        }
        
        loadGlobalSettings(); 
        loadBookmarks();
    }); 
// --- End of Client Logic ---
`

// --- Initialization ---
func initEnv() {
	listenPort = os.Getenv("PORT")
	if listenPort == "" {
		listenPort = "8080"
		log.Printf("Warning: PORT environment variable not set, defaulting to %s", listenPort)
	}

	authServiceURL = os.Getenv("AUTH_SERVICE_URL")
	if authServiceURL == "" {
		log.Fatal("Error: AUTH_SERVICE_URL environment variable must be set.")
	}
	if !strings.HasSuffix(authServiceURL, "/") {
		authServiceURL += "/"
	}
	log.Printf("Auth Service URL configured to: %s", authServiceURL)
}

// makeLandingPageHTML constructs the full HTML for the landing page.
func makeLandingPageHTML() string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Service Worker Web Proxy</title>
    <script src="/proxy?url=https%3A%2F%2Fcdn.tailwindcss.com"></script>
    <style type="text/css">`)
	sb.WriteString(styleCSSContent)
	sb.WriteString(`</style>
</head>
<body class="bg-gray-100 text-gray-800">
    <div class="container max-w-3xl mx-auto p-4 md:p-6">
        
        <div class="proxy-component bg-white p-4 sm:p-6 rounded-lg shadow-md border border-gray-200 mb-6"> 
            <h1 class="text-2xl sm:text-3xl font-bold text-center text-blue-700 mb-6">Service Worker Web Proxy</h1>
            <div class="url-input-container">
                <input type="url" id="url-input" name="url" placeholder="example.com or https://example.com" required 
                       class="block w-full p-3 border border-gray-300 rounded-md shadow-sm focus:ring-blue-500 focus:border-blue-500 text-base mb-2">
                
                <div class="flex items-center mt-2 mb-3"> <div id="global-settings-indicators" class="flex items-center space-x-1 sm:space-x-2 mr-2 sm:mr-3 text-xs sm:text-sm">
                        </div>
                    <button type="button" id="visit-btn" title="Visit & Auto-Bookmark"
                            class="flex-grow bg-blue-600 hover:bg-blue-700 text-white font-semibold py-3 px-4 rounded-md shadow-sm transition-colors duration-150 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-opacity-50">
                        Visit & Bookmark
                    </button>
                </div>
            </div>
            <div id="error-message" class="bg-red-100 border border-red-400 text-red-700 px-4 py-2 rounded-md mt-1 hidden text-sm"></div>
        </div>

        <div class="proxy-component bg-white p-4 sm:p-6 rounded-lg shadow-md border border-gray-200 mb-6"> 
            <h2 class="text-xl font-semibold text-blue-700 mb-4 border-b border-gray-300 pb-2">Bookmarks (Most Visited)</h2>
            <div id="bookmarks-list" class="divide-y divide-gray-200">
                </div>
            <button id="bookmark-current-site-btn" style="display:none;"
                    class="mt-4 bg-green-500 hover:bg-green-600 text-white font-semibold py-2 px-4 rounded-md shadow-sm transition-colors duration-150 text-sm">
                Bookmark Current Site
            </button>
        </div>

        <div class="proxy-component bg-white p-4 sm:p-6 rounded-lg shadow-md border border-gray-200"> 
            <details class="advanced-settings-section">
                <summary class="font-semibold py-2 cursor-pointer list-inside text-blue-700 text-lg hover:text-blue-800">
                    Global Privacy Settings
                </summary>
                <div class="advanced-settings-content mt-4 space-y-3">
                    <div class="settings-item bg-gray-50 p-3 rounded-md flex items-center justify-between text-sm">
                        <label for="global-js" class="text-gray-700">Allow JavaScript:</label>
                        <input type="checkbox" id="global-js" class="h-5 w-5 text-blue-600 border-gray-300 rounded focus:ring-blue-500">
                    </div>
                    <div class="settings-item bg-gray-50 p-3 rounded-md flex items-center justify-between text-sm">
                        <label for="global-cookies" class="text-gray-700">Allow Cookies:</label>
                        <input type="checkbox" id="global-cookies" class="h-5 w-5 text-blue-600 border-gray-300 rounded focus:ring-blue-500">
                    </div>
                    <div class="settings-item bg-gray-50 p-3 rounded-md flex items-center justify-between text-sm">
                        <label for="global-iframes" class="text-gray-700">Allow iFrames:</label>
                        <input type="checkbox" id="global-iframes" class="h-5 w-5 text-blue-600 border-gray-300 rounded focus:ring-blue-500">
                    </div>
                    <div class="settings-item bg-gray-50 p-3 rounded-md flex items-center justify-between text-sm">
                        <label for="global-raw-mode" class="text-gray-700">Raw Mode (No Server Rewrite):</label>
                        <input type="checkbox" id="global-raw-mode" class="h-5 w-5 text-blue-600 border-gray-300 rounded focus:ring-blue-500">
                    </div>
                </div>
            </details>
        </div>
    </div>
    <script type="text/javascript">//<![CDATA[
`)
	sb.WriteString(clientJSContentForEmbedding)
	sb.WriteString(`
//]]></script>
</body>
</html>`)
	return sb.String()
}


func main() {
	initEnv()

	http.HandleFunc("/auth/enter-email", handleServeEmailPage)
	http.HandleFunc("/auth/submit-email", handleSubmitEmailToExternalCF)
	http.HandleFunc("/auth/submit-code", handleSubmitCodeToExternalCF)
	http.HandleFunc(serviceWorkerPath, serveServiceWorkerJS)
	http.HandleFunc("/", masterHandler)

	log.Printf("Starting Service Worker Web Proxy server with auth on port %s", listenPort)
	if err := http.ListenAndServe(":"+listenPort, nil); err != nil {
		log.Fatalf("ListenAndServe error: %v", err)
	}
}

func serveServiceWorkerJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate") 
	w.Header().Set("Service-Worker-Allowed", "/") 
	fmt.Fprint(w, embeddedSWContent) 
}


// --- Utility Helper Functions ---

// generateSecureNonce creates a random base64 encoded string for CSP nonces.
// If random generation fails, it returns a hardcoded fallback nonce.
func generateSecureNonce() string {
	nonceBytes := make([]byte, 16) 
	_, err := rand.Read(nonceBytes)
	if err != nil {
		log.Printf("Error generating crypto/rand nonce: %v. Using fallback nonce.", err)
		return fallbackNonce
	}
	return base64.RawURLEncoding.EncodeToString(nonceBytes)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isCFAuthCookieValid(r *http.Request) (isValid bool, payload *JWTPayload, err error) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false, nil, nil 
	}
	return parseAndValidateJWT(cookie.Value)
}

func parseAndValidateJWT(cookieValue string) (isValid bool, payload *JWTPayload, err error) {
	parts := strings.Split(cookieValue, ".")
	if len(parts) != 3 {
		return false, nil, fmt.Errorf("token is not a valid JWT structure (parts != 3)")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, nil, fmt.Errorf("failed to base64-decode JWT payload: %w", err)
	}
	var p JWTPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return false, nil, fmt.Errorf("failed to unmarshal JWT payload JSON: %w", err)
	}
	now := time.Now().Unix()
	if p.ExpiresAt != 0 && now > p.ExpiresAt {
		return false, &p, fmt.Errorf("token expired at %s", time.Unix(p.ExpiresAt, 0))
	}
	if p.NotBefore != 0 && now < p.NotBefore {
		return false, &p, fmt.Errorf("token not yet valid (nbf: %s)", time.Unix(p.NotBefore, 0))
	}
	return true, &p, nil
}

func readAndDecompressBody(resp *http.Response) (bodyBytes []byte, wasGzipped bool, err error) {
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("reading body: %w", err)
	}
	contentEncoding := strings.ToLower(resp.Header.Get("Content-Encoding"))
	if contentEncoding == "gzip" {
		wasGzipped = true
		gzipReader, errGzip := gzip.NewReader(bytes.NewReader(bodyBytes))
		if errGzip != nil {
			log.Printf("Warning: Content-Encoding is gzip, but failed to create gzip reader: %v. Treating as uncompressed.", errGzip)
			return bodyBytes, false, nil
		}
		defer gzipReader.Close()
		decompressedBytes, errRead := io.ReadAll(gzipReader)
		if errRead != nil {
			return bodyBytes, true, fmt.Errorf("decompressing gzip body: %w", errRead) 
		}
		return decompressedBytes, true, nil
	}
	return bodyBytes, false, nil
}

func parseGeneralForm(htmlBody string, specificFormRegex *regexp.Regexp) (actionURL string, hiddenFields url.Values, formFound bool) {
	hiddenFields = url.Values{}
	if specificFormRegex != nil {
		matches := specificFormRegex.FindStringSubmatch(htmlBody)
		if len(matches) > 0 {
			if len(matches) > 1 {
				actionURL = matches[1]
			}
			formFound = true
		}
	}
	if actionURL == "" { 
		actionMatches := formActionRegex.FindStringSubmatch(htmlBody)
		if len(actionMatches) > 1 {
			actionURL = actionMatches[1]
			formFound = true 
		}
	}
	if !formFound {
		log.Println("Warning: parseGeneralForm: Could not find any form tag matching criteria.")
	}

	hiddenInputMatches := hiddenInputRegex.FindAllStringSubmatch(htmlBody, -1)
	for _, match := range hiddenInputMatches {
		if len(match) == 3 { 
			fieldName := stdhtml.UnescapeString(strings.TrimSpace(match[1]))
			fieldValue := stdhtml.UnescapeString(strings.TrimSpace(match[2]))
			hiddenFields.Add(fieldName, fieldValue)
		}
	}
	return
}

// --- Request/Response Manipulation Helpers (Auth Flow) ---
func setupBasicHeadersForAuth(proxyReq *http.Request, clientReq *http.Request, destHost string) {
	proxyReq.Header.Set("Host", destHost)
	proxyReq.Header.Set("User-Agent", "PrivacyProxyAuthFlow/1.0 (Appspot)")
	proxyReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	proxyReq.Header.Set("Accept-Language", clientReq.Header.Get("Accept-Language")) 
	proxyReq.Header.Set("Accept-Encoding", "gzip, deflate")                       
	proxyReq.Header.Del("Cookie")                                                 

	clientIP := strings.Split(clientReq.RemoteAddr, ":")[0]
	if existingXFF := clientReq.Header.Get("X-Forwarded-For"); existingXFF != "" {
		proxyReq.Header.Set("X-Forwarded-For", existingXFF+", "+clientIP)
	} else {
		proxyReq.Header.Set("X-Forwarded-For", clientIP)
	}
	if clientReq.Header.Get("X-Forwarded-Proto") != "" {
		proxyReq.Header.Set("X-Forwarded-Proto", clientReq.Header.Get("X-Forwarded-Proto"))
	} else if clientReq.TLS != nil {
		proxyReq.Header.Set("X-Forwarded-Proto", "https")
	} else {
		proxyReq.Header.Set("X-Forwarded-Proto", "http")
	}
	proxyReq.Header.Set("X-Forwarded-Host", clientReq.Host) 
}

func addCookiesToOutgoingRequest(outgoingReq *http.Request, setCookieHeaders []string) {
	if len(setCookieHeaders) == 0 {
		return
	}
	tempRespHeader := http.Header{"Set-Cookie": setCookieHeaders}
	dummyResp := http.Response{Header: tempRespHeader}

	existingCookies := make(map[string]string)
	for _, c := range outgoingReq.Cookies() {
		existingCookies[c.Name] = c.Value
	}

	for _, newCookie := range dummyResp.Cookies() {
		existingCookies[newCookie.Name] = newCookie.Value
	}

	outgoingReq.Header.Del("Cookie") 
	var cookiePairs []string
	for name, value := range existingCookies {
		cookiePairs = append(cookiePairs, name+"="+value)
	}
	if len(cookiePairs) > 0 {
		outgoingReq.Header.Set("Cookie", strings.Join(cookiePairs, "; "))
	}
}

// --- Auth Flow Page Servers ---
func serveCustomCodeInputPage(w http.ResponseWriter, r *http.Request, nonce, cfCallbackURL string, setCookieHeaders []string, cfAccessDomain string) {
	log.Printf("Serving custom code input page. Nonce: %s, CF_Callback: %s, CF_Access_Domain: %s", nonce, cfCallbackURL, cfAccessDomain)
	for _, ch := range setCookieHeaders { 
		w.Header().Add("Set-Cookie", ch)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Enter Verification Code</title><style>body{font-family:sans-serif;margin:20px;display:flex;flex-direction:column;align-items:center;padding-top:40px;background-color:#f0f2f5;}.container{border:1px solid #ccc;padding:20px 30px;border-radius:8px;background-color:#fff;box-shadow:0 2px 10px rgba(0,0,0,0.1);}form > div{margin-bottom:15px;}label{display:inline-block;min-width:120px;margin-bottom:5px;}input[type="text"],input[type="email"]{padding:10px;border:1px solid #ddd;border-radius:4px;width:250px;}button{padding:10px 15px;background-color:#007bff;color:white;border:none;border-radius:4px;cursor:pointer;font-size:1em;}button:hover{background-color:#0056b3;}</style></head><body><div class="container"><h2>Enter Verification Code</h2><p>A code was sent to your email. Please enter it below.</p><form action="/auth/submit-code" method="POST"><input type="hidden" name="nonce" value="`)
	sb.WriteString(stdhtml.EscapeString(nonce))
	sb.WriteString(`"><input type="hidden" name="cf_callback_url" value="`)
	sb.WriteString(stdhtml.EscapeString(cfCallbackURL))
	sb.WriteString(`"><div><label for="code">Verification Code:</label><input type="text" id="code" name="code" pattern="\d{6}" title="Enter the 6-digit code" required maxlength="6" inputmode="numeric" autofocus></div><div><button type="submit">Submit Code</button></div></form></div></body></html>`)
	fmt.Fprint(w, sb.String())
}

func handleServeEmailPage(w http.ResponseWriter, r *http.Request) {
	log.Println("Serving custom email entry page for proxy auth.")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	originalURL := "/" 
	if origURLCookie, err := r.Cookie("proxy-original-url"); err == nil { 
		if unescaped, errUnescape := url.QueryUnescape(origURLCookie.Value); errUnescape == nil {
			originalURL = unescaped
		}
	}

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><title>Proxy Authentication - Enter Email</title><style>body{font-family:sans-serif;margin:20px;display:flex;flex-direction:column;align-items:center;padding-top:40px;background-color:#f0f2f5;}.container{border:1px solid #ccc;padding:20px 30px;border-radius:8px;background-color:#fff;box-shadow:0 2px 10px rgba(0,0,0,0.1);}form > div{margin-bottom:15px;}label{display:inline-block;min-width:120px;margin-bottom:5px;}input[type="text"],input[type="email"]{padding:10px;border:1px solid #ddd;border-radius:4px;width:250px;}button{padding:10px 15px;background-color:#007bff;color:white;border:none;border-radius:4px;cursor:pointer;font-size:1em;}button:hover{background-color:#0056b3;}</style></head><body><div class="container"><h2>Proxy Service Authentication</h2><p>Please enter your email to access the proxy service:</p><form action="/auth/submit-email" method="POST"><input type="hidden" name="original_url" value="`)
	sb.WriteString(stdhtml.EscapeString(originalURL)) 
	sb.WriteString(`"><div><label for="email">Email:</label><input type="email" id="email" name="email" required autofocus></div><div><button type="submit">Send Verification Code</button></div></form></div></body></html>`)
	fmt.Fprint(w, sb.String())
}

// --- Auth Flow Submission Handlers ---
func handleSubmitEmailToExternalCF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing email form: "+err.Error(), http.StatusBadRequest)
		return
	}
	userEmail := r.FormValue("email")
	originalURLPath := r.FormValue("original_url") 
	if originalURLPath == "" {
		originalURLPath = "/" 
	}
	if userEmail == "" {
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}
	log.Printf("Auth: Email submitted: %s. Original proxy URL intended: %s", userEmail, originalURLPath)

	log.Printf("Auth: Fetching external CF Access login page from: %s", authServiceURL)
	tempReq, _ := http.NewRequest(http.MethodGet, authServiceURL, nil)
	parsedAuthServiceURL, _ := url.Parse(authServiceURL) 
	setupBasicHeadersForAuth(tempReq, r, parsedAuthServiceURL.Host)

	tempClient := &http.Client{Timeout: 20 * time.Second} 
	cfLoginPageResp, err := tempClient.Do(tempReq)
	if err != nil {
		http.Error(w, "Failed to fetch external CF Access login page: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer cfLoginPageResp.Body.Close()

	var currentSetCookieHeaders = cfLoginPageResp.Header["Set-Cookie"] 

	cfLoginPageBodyBytes, _, err := readAndDecompressBody(cfLoginPageResp)
	if err != nil {
		http.Error(w, "Failed to read external CF Access login page body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	htmlBody := string(cfLoginPageBodyBytes)

	formActionRaw, hiddenFields, formFound := parseGeneralForm(htmlBody, nil) 
	if !formFound || formActionRaw == "" {
		log.Printf("Could not find form on external CF Access page from %s. Body snippet: %s", cfLoginPageResp.Request.URL.String(), htmlBody[:min(500, len(htmlBody))])
		http.Error(w, "Failed to find email submission form on external Cloudflare page.", http.StatusInternalServerError)
		return
	}
	formActionDecoded := stdhtml.UnescapeString(formActionRaw)
	emailFormActionURL, err := cfLoginPageResp.Request.URL.Parse(formActionDecoded) 
	if err != nil {
		log.Printf("Error resolving email form action URL '%s' from external CF page: %v", formActionDecoded, err)
		http.Error(w, "Invalid email submission form action on external Cloudflare page.", http.StatusInternalServerError)
		return
	}
	log.Printf("Auth: Email form action URL from external CF page resolved to: %s", emailFormActionURL.String())

	formData := url.Values{"email": {userEmail}}
	for name, values := range hiddenFields {
		for _, value := range values {
			formData.Add(name, value)
		}
	}
	encodedEmailFormData := formData.Encode()

	automatedPostReq, _ := http.NewRequest(http.MethodPost, emailFormActionURL.String(), strings.NewReader(encodedEmailFormData))
	setupBasicHeadersForAuth(automatedPostReq, r, emailFormActionURL.Host)
	automatedPostReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	automatedPostReq.Header.Set("Origin", fmt.Sprintf("%s://%s", emailFormActionURL.Scheme, emailFormActionURL.Host))
	automatedPostReq.Header.Set("Referer", cfLoginPageResp.Request.URL.String()) 
	automatedPostReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(encodedEmailFormData)))
	addCookiesToOutgoingRequest(automatedPostReq, currentSetCookieHeaders) 

	log.Printf(">>> Sending automated email POST to %s", emailFormActionURL.String())

	emailSubmitClient := &http.Client{Timeout: 20 * time.Second} 
	respAfterEmailPost, err := emailSubmitClient.Do(automatedPostReq)
	if err != nil {
		log.Printf("Error POSTing email to external CF Access %s: %v", emailFormActionURL.String(), err)
		http.Error(w, "Failed to submit email to external Cloudflare: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer respAfterEmailPost.Body.Close()

	log.Printf("<<< Received response from automated email POST to %s: Status %s", respAfterEmailPost.Request.URL.String(), respAfterEmailPost.Status)
	currentSetCookieHeaders = append(currentSetCookieHeaders, respAfterEmailPost.Header["Set-Cookie"]...) 

	bodyAfterEmailPost, _, err := readAndDecompressBody(respAfterEmailPost)
	if err != nil {
		http.Error(w, "Error reading response after email POST to external CF: "+err.Error(), http.StatusInternalServerError)
		return
	}
	htmlAfterEmailPost := string(bodyAfterEmailPost)

	codeFormActionRaw, codeFormHiddenFields, codeFormFound := parseGeneralForm(htmlAfterEmailPost, codeInputFormRegex)
	var nonceValue string
	nonceMatches := nonceInputRegex.FindStringSubmatch(htmlAfterEmailPost) 
	if len(nonceMatches) > 1 {
		nonceValue = stdhtml.UnescapeString(nonceMatches[1])
		if _, ok := codeFormHiddenFields["nonce"]; !ok { 
			codeFormHiddenFields.Add("nonce", nonceValue)
		}
	} else if val, ok := codeFormHiddenFields["nonce"]; ok && len(val) > 0 { 
		nonceValue = val[0]
	}

	if codeFormFound && nonceValue != "" && (strings.Contains(htmlAfterEmailPost, "Enter code") || strings.Contains(htmlAfterEmailPost, "Enter the code") || strings.Contains(htmlAfterEmailPost, "Verification code")) {
		log.Println("Auth: Detected 'Enter Code' page from external CF. Serving custom code input page.")
		codeFormActionDecoded := stdhtml.UnescapeString(codeFormActionRaw)
		baseForCodeCallback := respAfterEmailPost.Request.URL         
		parsedCodeCallbackURL, err := baseForCodeCallback.Parse(codeFormActionDecoded) 
		if err != nil {
			log.Printf("Auth: Error resolving code callback URL '%s' against base '%s': %v", codeFormActionDecoded, baseForCodeCallback.String(), err)
			http.Error(w, "Invalid code submission form action on external Cloudflare page.", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "proxy-original-url", Value: url.QueryEscape(originalURLPath), Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode, MaxAge: 300})
		serveCustomCodeInputPage(w, r, nonceValue, parsedCodeCallbackURL.String(), currentSetCookieHeaders, baseForCodeCallback.Host)
		return
	}

	log.Println("Auth: Did not detect 'Enter Code' page after email submission to external CF. Content received (first 1KB):")
	log.Println(htmlAfterEmailPost[:min(1000, len(htmlAfterEmailPost))])
	http.Error(w, "Failed to reach the 'Enter Code' page from external Cloudflare. Please check logs and try again.", http.StatusInternalServerError)
}

func handleSubmitCodeToExternalCF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing code form: "+err.Error(), http.StatusBadRequest)
		return
	}
	userCode := r.FormValue("code")
	nonce := r.FormValue("nonce")
	cfCallbackURLString := r.FormValue("cf_callback_url")

	if userCode == "" || nonce == "" || cfCallbackURLString == "" {
		http.Error(w, "Missing code, nonce, or callback URL", http.StatusBadRequest)
		return
	}
	log.Printf("Auth: Received code for external CF. Code: %s..., Nonce: %s..., CF_Callback_URL: %s", userCode[:min(2, len(userCode))], nonce[:min(10, len(nonce))], cfCallbackURLString)

	cfFormData := url.Values{"code": {userCode}, "nonce": {nonce}}
	encodedCfFormData := cfFormData.Encode()

	currentRedirectURLString := cfCallbackURLString
	var accumulatedSetCookies []string 

	for _, cookie := range r.Cookies() {
		if cookie.Name != "proxy-original-url" && cookie.Name != authCookieName { 
			accumulatedSetCookies = append(accumulatedSetCookies, cookie.String()) 
		}
	}

	loopClient := &http.Client{
		Timeout: 20 * time.Second, 
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			log.Printf(">>> Auth redirect loop: Client was about to redirect from %s to %s", via[len(via)-1].URL.String(), req.URL.String())
			return http.ErrUseLastResponse 
		},
	}
	var finalLoopResponse *http.Response 

	for i := 0; i < maxRedirects; i++ {
		log.Printf("Auth redirect loop (Attempt %d): Requesting %s", i+1, currentRedirectURLString)
		var reqToFollow *http.Request
		var err error

		if i == 0 { 
			reqToFollow, err = http.NewRequest(http.MethodPost, currentRedirectURLString, strings.NewReader(encodedCfFormData))
			if err != nil {
				http.Error(w, "Error creating POST for code to external CF: "+err.Error(), http.StatusInternalServerError)
				return
			}
			reqToFollow.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			reqToFollow.Header.Set("Content-Length", fmt.Sprintf("%d", len(encodedCfFormData)))
		} else { 
			reqToFollow, err = http.NewRequest(http.MethodGet, currentRedirectURLString, nil)
			if err != nil {
				http.Error(w, "Error creating GET for redirect to external CF: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		parsedCurrentURL, _ := url.Parse(currentRedirectURLString) 
		setupBasicHeadersForAuth(reqToFollow, r, parsedCurrentURL.Host)
		
		var rawCookieStringsForHeader []string
		tempRespHeader := http.Header{"Set-Cookie": accumulatedSetCookies}
		dummyResp := http.Response{Header: tempRespHeader}
		for _, ck := range dummyResp.Cookies() {
			rawCookieStringsForHeader = append(rawCookieStringsForHeader, ck.Name+"="+ck.Value)
		}
		if len(rawCookieStringsForHeader) > 0 {
			reqToFollow.Header.Set("Cookie", strings.Join(rawCookieStringsForHeader, "; "))
		}

		if i == 0 {
			log.Printf(">>> Sending final auth request (code POST) to %s", currentRedirectURLString)
		} else {
			log.Printf(">>> Auth redirect loop (Attempt %d) GET %s", i+1, currentRedirectURLString)
		}

		resp, err := loopClient.Do(reqToFollow)
		if err != nil {
			log.Printf("Error in auth redirect loop (Attempt %d) for %s: %v", i+1, currentRedirectURLString, err)
			if resp == nil { 
				http.Error(w, "Error during external CF redirect following: "+err.Error(), http.StatusBadGateway)
				return
			}
		}
		
		if resp != nil {
			log.Printf("<<< Auth redirect loop (Attempt %d) Response from %s: Status %s", i+1, resp.Request.URL.String(), resp.Status)
			if sc := resp.Header["Set-Cookie"]; len(sc) > 0 {
				log.Printf("    Accumulating %d Set-Cookie headers from external CF step.", len(sc))
				accumulatedSetCookies = append(accumulatedSetCookies, sc...)
			}
			finalLoopResponse = resp 
			
			if resp.StatusCode >= 300 && resp.StatusCode <= 308 && resp.StatusCode != http.StatusNotModified {
				location := resp.Header.Get("Location")
				if location == "" {
					log.Printf("Auth redirect status %d but no Location header. Breaking loop.", resp.StatusCode)
					resp.Body.Close()
					break 
				}
				resolvedLocationURL, err := resp.Request.URL.Parse(location) 
				if err != nil {
					log.Printf("Error parsing external CF redirect Location '%s': %v. Breaking loop.", location, err)
					resp.Body.Close()
					break
				}
				currentRedirectURLString = resolvedLocationURL.String()
				log.Printf("    Following external CF redirect to: %s", currentRedirectURLString)
				resp.Body.Close() 
				continue          
			} else {
				log.Printf("Auth redirect loop finished. Final status from external CF: %s", resp.Status)
				break 
			}
		} else { 
			log.Println("Auth: Error: No response object in redirect loop despite no error.")
			http.Error(w, "Internal error during authentication.", http.StatusInternalServerError)
			return
		}
	} 

	if finalLoopResponse == nil {
		log.Println("Auth: Error: No final response obtained from external CF redirect loop.")
		http.Error(w, "Failed to complete authentication with external Cloudflare service.", http.StatusInternalServerError)
		return
	}
	defer finalLoopResponse.Body.Close() 

	var actualCfAuthJWTValue string
	var decodedJWTPayload *JWTPayload
	var cfAuthCookieToSet *http.Cookie 

	tempRespHeaderForParsing := http.Header{"Set-Cookie": accumulatedSetCookies}
	dummyRespForParsing := http.Response{Header: tempRespHeaderForParsing}
	for _, parsedCookie := range dummyRespForParsing.Cookies() {
		if parsedCookie.Name == authCookieName {
			actualCfAuthJWTValue = parsedCookie.Value
			_, decodedJWTPayload, _ = parseAndValidateJWT(actualCfAuthJWTValue) 
			cfAuthCookieToSet = parsedCookie
			break 
		}
	}

	if actualCfAuthJWTValue != "" && cfAuthCookieToSet != nil {
		log.Printf("Auth: Successfully obtained actual CF_Authorization JWT from external CF. Value: %s...", actualCfAuthJWTValue[:min(30, len(actualCfAuthJWTValue))])
		
		cfAuthCookieToSet.Domain = "" 
		cfAuthCookieToSet.Path = "/"  
		cfAuthCookieToSet.Secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		if cfAuthCookieToSet.SameSite == http.SameSiteDefaultMode {
			cfAuthCookieToSet.SameSite = http.SameSiteLaxMode
		}
		http.SetCookie(w, cfAuthCookieToSet)
		log.Printf("Auth: Set proxy's %s cookie. Name: %s, Path: %s, Secure: %t, HttpOnly: %t, SameSite: %v, MaxAge: %d",
			authCookieName, cfAuthCookieToSet.Name, cfAuthCookieToSet.Path, cfAuthCookieToSet.Secure, cfAuthCookieToSet.HttpOnly, cfAuthCookieToSet.SameSite, cfAuthCookieToSet.MaxAge)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		http.SetCookie(w, &http.Cookie{Name: "proxy-original-url", Value: "", Path: "/", MaxAge: -1})

		var body strings.Builder
		body.WriteString("<h1>Proxy Authentication Successful!</h1><p>You can now use the proxy service.</p>")
		if decodedJWTPayload != nil {
			body.WriteString("<h2>Decoded JWT Payload (from external CF):</h2><pre>")
			payloadBytes, _ := json.MarshalIndent(decodedJWTPayload, "", "  ")
			body.WriteString(stdhtml.EscapeString(string(payloadBytes)))
			body.WriteString("</pre>")
		}
		originalURLPath := "/" 
		if origURLCookie, errCookie := r.Cookie("proxy-original-url"); errCookie == nil { 
			if unescaped, errUnescape := url.QueryUnescape(origURLCookie.Value); errUnescape == nil {
				originalURLPath = unescaped
			}
		}
		body.WriteString(fmt.Sprintf("<p><a href=\"%s\">Continue to your page</a> or <a href=\"/\">Go to Proxy Home</a></p>", stdhtml.EscapeString(originalURLPath)))
		fmt.Fprint(w, body.String())
	} else {
		log.Println("Auth: CF_Authorization JWT not found in accumulated cookies after external CF code submission.")
		finalBodyBytes, _, _ := readAndDecompressBody(finalLoopResponse) 
		passThroughResponse(w, r.Host, finalLoopResponse, finalBodyBytes, accumulatedSetCookies, false) 
	}
}


// --- Privacy Proxy Core Handlers & Helpers ---

func getBoolCookie(r *http.Request, name string) bool {
	cookie, err := r.Cookie(name)
	if err != nil {
		return false 
	}
	return cookie.Value == "true"
}


func rewriteProxiedURL(originalAttrURL string, pageBaseURL *url.URL, clientReq *http.Request) (string, error) {
	originalAttrURL = strings.TrimSpace(originalAttrURL)
	if originalAttrURL == "" || strings.HasPrefix(originalAttrURL, "#") ||
		strings.HasPrefix(originalAttrURL, "javascript:") ||
		strings.HasPrefix(originalAttrURL, "mailto:") ||
		strings.HasPrefix(originalAttrURL, "tel:") ||
		strings.HasPrefix(originalAttrURL, "data:") || 
		strings.HasPrefix(originalAttrURL, "blob:") { 
		return originalAttrURL, nil
	}

	absURL, err := pageBaseURL.Parse(originalAttrURL) 
	if err != nil {
		tempAbsURL, err2 := url.Parse(originalAttrURL)
		if err2 == nil && (tempAbsURL.Scheme == "http" || tempAbsURL.Scheme == "https") {
			absURL = tempAbsURL 
		} else {
			log.Printf("Error parsing attribute URL '%s' against base '%s': %v. Also failed as absolute: %v", originalAttrURL, pageBaseURL.String(), err, err2)
			return originalAttrURL, err 
		}
	}

	if absURL.Scheme != "http" && absURL.Scheme != "https" {
		return absURL.String(), nil 
	}

	proxyScheme := "http"
	if clientReq.TLS != nil || clientReq.Header.Get("X-Forwarded-Proto") == "https" {
		proxyScheme = "https"
	}
	proxyAccessURL := fmt.Sprintf("%s://%s%s?url=%s",
		proxyScheme,
		clientReq.Host, 
		proxyRequestPath,
		url.QueryEscape(absURL.String()),
	)
	return proxyAccessURL, nil
}

func rewriteHTMLContentAdvanced(htmlReader io.Reader, pageBaseURL *url.URL, clientReq *http.Request, prefs sitePreferences, scriptNonce string) (io.Reader, error) {
	doc, err := html.Parse(htmlReader)
	if err != nil {
		return nil, fmt.Errorf("HTML parsing error: %w", err)
	}

	// Phase 1: Rewrite existing nodes for proxying and applying preferences
	var rewriteExistingContentFunc func(*html.Node)
	rewriteExistingContentFunc = func(n *html.Node) {
		if n.Type == html.ElementNode {
			// Handle script tags based on JavaScriptEnabled preference
			if n.Data == "script" {
				if !prefs.JavaScriptEnabled {
					// Change type to prevent execution and remove content
					n.Attr = []html.Attribute{{Key: "type", Val: "text/inert-script"}}
					for c := n.FirstChild; c != nil; {
						next := c.NextSibling
						n.RemoveChild(c)
						c = next
					}
				} else {
					// If JS is enabled, rewrite src attribute if present
					for i, attr := range n.Attr {
						if strings.ToLower(attr.Key) == "src" && attr.Val != "" {
							if proxiedURL, err := rewriteProxiedURL(attr.Val, pageBaseURL, clientReq); err == nil && proxiedURL != attr.Val {
								n.Attr[i].Val = proxiedURL
							}
						}
					}
				}
			} else if n.Data == "iframe" || n.Data == "frame" { // Handle iframe/frame tags
				if !prefs.IframesEnabled {
					// If iframes are disabled, set src to about:blank
					n.Attr = []html.Attribute{{Key: "src", Val: "about:blank"}}
				} else {
					// If iframes are enabled, rewrite src attribute
					for i, attr := range n.Attr {
						if strings.ToLower(attr.Key) == "src" && attr.Val != "" {
							if proxiedURL, err := rewriteProxiedURL(attr.Val, pageBaseURL, clientReq); err == nil && proxiedURL != attr.Val {
								n.Attr[i].Val = proxiedURL
							}
						}
					}
				}
			} else {
				// General attribute rewriting for other elements
				var newAttrs []html.Attribute
				for _, attr := range n.Attr {
					currentAttr := attr
					attrKeyLower := strings.ToLower(currentAttr.Key)
					attrVal := strings.TrimSpace(currentAttr.Val)
					
					shouldRewrite := false
					switch attrKeyLower {
					case "href", "src", "action", "longdesc", "cite", "formaction", "icon", "manifest", "poster", "data", "background":
						if attrVal != "" {
							shouldRewrite = true
						}
					case "srcset":
						if attrVal != "" {
							sources := strings.Split(attrVal, ",")
							var newSources []string
							changed := false
							for _, source := range sources {
								trimmedSource := strings.TrimSpace(source)
								parts := strings.Fields(trimmedSource)
								if len(parts) > 0 {
									u := parts[0]
									descriptor := ""
									if len(parts) > 1 {
										descriptor = " " + strings.Join(parts[1:], " ")
									}
									if proxiedU, err := rewriteProxiedURL(u, pageBaseURL, clientReq); err == nil && proxiedU != u {
										newSources = append(newSources, proxiedU+descriptor)
										changed = true
									} else {
										newSources = append(newSources, source) 
									}
								} else {
									newSources = append(newSources, source) 
								}
							}
							if changed {
								currentAttr.Val = strings.Join(newSources, ", ")
							}
						}
					case "style":
						if attrVal != "" {
							newStyleVal := rewriteCSSURLsInString(attrVal, pageBaseURL, clientReq)
							if newStyleVal != attrVal {
								currentAttr.Val = newStyleVal
							}
						}
					case "target":
						if strings.ToLower(attrVal) == "_blank" {
							currentAttr.Val = "_self" 
						}
					case "integrity", "crossorigin":
						continue 
					}

					if shouldRewrite {
						if proxiedURL, err := rewriteProxiedURL(attrVal, pageBaseURL, clientReq); err == nil && proxiedURL != attrVal {
							currentAttr.Val = proxiedURL
						} else if err != nil {
							log.Printf("HTML Rewrite (Phase 1): Error proxying URL for attr '%s' val '%s' (base '%s'): %v", attrKeyLower, attrVal, pageBaseURL.String(), err)
						}
					}
					
					if strings.HasPrefix(attrKeyLower, "on") && !prefs.JavaScriptEnabled {
						continue 
					}
					newAttrs = append(newAttrs, currentAttr)
				}
				n.Attr = newAttrs
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rewriteExistingContentFunc(c)
		}
	}
	rewriteExistingContentFunc(doc) 

	var bodyNode *html.Node
	var findBodyNodeFunc func(*html.Node)
	findBodyNodeFunc = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "body" {
			bodyNode = n
			return 
		}
		for c := n.FirstChild; c != nil && bodyNode == nil; c = c.NextSibling {
			findBodyNodeFunc(c)
		}
	}
	findBodyNodeFunc(doc)

	if bodyNode != nil {
		injectedHTML := makeInjectedHTML(scriptNonce) 
		parsedNodes, errFrag := html.ParseFragment(strings.NewReader(injectedHTML), bodyNode)
		if errFrag != nil {
			log.Printf("ERROR parsing HTML fragment for injection (Phase 2): %v. HTML: %s", errFrag, injectedHTML)
		} else {
			for _, nodeToAdd := range parsedNodes {
				bodyNode.AppendChild(nodeToAdd)
			}
		}
	} else {
		log.Println("Warning: <body> tag not found in HTML document. Cannot inject proxy home button or script.")
	}

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("HTML rendering error after all phases: %w", err)
	}
	return &buf, nil
}


func rewriteCSSURLsInString(cssContent string, baseURL *url.URL, clientReq *http.Request) string {
	return cssURLRegex.ReplaceAllStringFunc(cssContent, func(match string) string {
		subMatches := cssURLRegex.FindStringSubmatch(match)
		var rawURL string
		if len(subMatches) > 1 {
			if subMatches[1] != "" { rawURL = subMatches[1] 
			} else if subMatches[2] != "" { rawURL = subMatches[2] 
			} else if subMatches[3] != "" { rawURL = subMatches[3] 
			}
		}
		if rawURL == "" || strings.HasPrefix(strings.ToLower(rawURL), "data:") { 
			return match 
		}

		proxiedURL, err := rewriteProxiedURL(rawURL, baseURL, clientReq)
		if err == nil && proxiedURL != rawURL {
			if subMatches[1] != "" { return fmt.Sprintf("url('%s')", proxiedURL)
			} else if subMatches[2] != "" { return fmt.Sprintf("url(\"%s\")", proxiedURL)
			} else { return fmt.Sprintf("url('%s')", proxiedURL) 
			}
		}
		return match
	})
}

// generateCSP creates the Content-Security-Policy for proxied content.
func generateCSP(prefs sitePreferences, targetURL *url.URL, clientReq *http.Request, scriptNonce string) string {
	directives := map[string]string{
		"default-src": "'none'", 
		"object-src":  "'none'",
		"base-uri":    "'self'", 
		"form-action": "'self'", 
		"manifest-src": "'none'", 
	}

	scriptSrcElements := []string{} 
	// Add nonce for our injected script. This is always added as generateSecureNonce() returns a value.
	// The script itself is only injected if Raw Mode is OFF for HTML.
	scriptSrcElements = append(scriptSrcElements, fmt.Sprintf("'nonce-%s'", scriptNonce))
	
	if prefs.JavaScriptEnabled {
		// If JS is enabled for the site, allow 'self' for the site's own scripts (which are rewritten to be from 'self')
		// and also unsafe-inline/eval for the site's inline/eval'd scripts.
		scriptSrcElements = append(scriptSrcElements, "'self'", "'unsafe-inline'", "'unsafe-eval'")
	}
	// If JS is disabled, only 'nonce-...' will be in scriptSrcElements.
	// This allows our injected script (if present) but blocks other scripts from 'self' or inline/eval from the target page.

	directives["script-src"] = strings.Join(scriptSrcElements, " ")
	directives["worker-src"] = "'self'" 

	styleSrc := []string{"'self'", "'unsafe-inline'", "*"} 
	directives["style-src"] = strings.Join(styleSrc, " ")

	imgSrc := []string{"'self'", "data:", "blob:", "*"} 
	directives["img-src"] = strings.Join(imgSrc, " ")

	fontSrc := []string{"'self'", "data:", "*"} 
	directives["font-src"] = strings.Join(fontSrc, " ")

	connectSrc := []string{"'self'"} 
	directives["connect-src"] = strings.Join(connectSrc, " ")
	
	if prefs.IframesEnabled {
		directives["frame-src"] = "'self' data: blob:" 
	} else {
		directives["frame-src"] = "'none'"
	}
	directives["child-src"] = directives["frame-src"] 

	mediaSrc := []string{"'self'", "blob:"}
	directives["media-src"] = strings.Join(mediaSrc, " ")
	
	var cspParts []string
	for directive, value := range directives {
		cspParts = append(cspParts, fmt.Sprintf("%s %s", directive, value))
	}
	return strings.Join(cspParts, "; ")
}


func handleLandingPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Log specific App Engine geo headers for the landing page request
	country := r.Header.Get("X-Appengine-Country")
	region := r.Header.Get("X-Appengine-Region")
	city := r.Header.Get("X-Appengine-City")
	if country != "" || region != "" || city != "" {
		log.Printf("Landing Page Geo Headers: Country=%s, Region=%s, City=%s", country, region, city)
	} else {
		log.Println("Landing Page: No App Engine geo headers found.")
	}


	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	cspHeader := []string{
		"default-src 'self'", 
		"script-src 'self' 'unsafe-inline' 'unsafe-eval'", 
		"style-src 'self' 'unsafe-inline'",                
		"img-src 'self' data: blob:",                      
		"font-src 'self' data:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"connect-src 'self'", 
		"frame-src 'none'",
	}
	w.Header().Set("Content-Security-Policy", strings.Join(cspHeader, "; "))
	
	fmt.Fprint(w, makeLandingPageHTML())
}

// setupOutgoingHeadersForProxy configures headers for the request to the target server.
func setupOutgoingHeadersForProxy(proxyToTargetReq *http.Request, clientToProxyReq *http.Request, targetURL *url.URL, prefs sitePreferences) {
	targetHost := targetURL.Host
	// Copy relevant headers from client to proxy request, filtering sensitive ones.
	for name, values := range clientToProxyReq.Header {
		lowerName := strings.ToLower(name)

		switch lowerName {
		// Skip headers set explicitly later or are hop-by-hop/problematic.
		case "host", "cookie", "referer", "origin":
			continue
		case "accept-encoding": 
			continue 
		case "connection", "keep-alive", "proxy-authenticate", "proxy-connection",
			"te", "trailers", "transfer-encoding", "upgrade":
			continue
		case "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto",
			"x-real-ip", "forwarded", "via": // These are often set by GAE; we don't want to pass GAE's versions.
			continue
		case "proxy-authorization":
			continue
		}

		// Filter out Sec- headers, except for Sec-CH-* (Client Hints)
		if strings.HasPrefix(lowerName, "sec-") {
			if strings.HasPrefix(lowerName, "sec-ch-") {
				for _, value := range values {
					proxyToTargetReq.Header.Add(name, value)
				}
			}
			continue // Skip other Sec- headers
		}
		
		// Filter out Appspot/Google Cloud specific headers
		if strings.HasPrefix(lowerName, "x-appengine-") || 
		   strings.HasPrefix(lowerName, "x-google-") || // General Google headers
		   lowerName == "x-cloud-trace-context" {
			// No longer logging the stripping of each header for cleaner logs
			continue
		}

		for _, value := range values {
			proxyToTargetReq.Header.Add(name, value)
		}
	}

	proxyToTargetReq.Header.Set("Host", targetHost)

	// Handle Cookies based on preferences
	proxyToTargetReq.Header.Del("Cookie") 
	if prefs.CookiesEnabled {
		var cookiesToSend []string
		for _, cookie := range clientToProxyReq.Cookies() {
			lowerCookieName := strings.ToLower(cookie.Name)
			if strings.HasPrefix(lowerCookieName, "proxy-") ||
				strings.HasPrefix(lowerCookieName, "cf_") {
				continue
			}
			cookiesToSend = append(cookiesToSend, cookie.Name+"="+cookie.Value)
		}
		if len(cookiesToSend) > 0 {
			proxyToTargetReq.Header.Set("Cookie", strings.Join(cookiesToSend, "; "))
		}
	}

	// Handle Referer Header:
	proxyToTargetReq.Header.Del("Referer") 
	clientReferer := clientToProxyReq.Header.Get("Referer")
	if clientReferer != "" {
		refererURL, err := url.Parse(clientReferer)
		if err == nil {
			if refererURL.Host == clientToProxyReq.Host && strings.HasPrefix(refererURL.Path, proxyRequestPath) {
				originalReferer := refererURL.Query().Get("url") 
				if originalReferer != "" {
					if parsedOriginalReferer, errParse := url.Parse(originalReferer); errParse == nil && (parsedOriginalReferer.Scheme == "http" || parsedOriginalReferer.Scheme == "https") {
						proxyToTargetReq.Header.Set("Referer", originalReferer)
						log.Printf("Referer: Transformed proxy referer to: %s", originalReferer)
					} else {
						log.Printf("Referer: Extracted original referer '%s' is invalid. Referer removed.", originalReferer)
					}
				} else {
					log.Printf("Referer: Proxy referer '%s' did not contain 'url' query param. Referer removed.", clientReferer)
				}
			} else if refererURL.Host == clientToProxyReq.Host && (refererURL.Path == "/" || refererURL.Path == "") {
				log.Printf("Referer: Is from proxy landing page ('%s'). Referer removed for request to target.", clientReferer)
			} else {
				if refererURL.Scheme == "http" || refererURL.Scheme == "https" {
					proxyToTargetReq.Header.Set("Referer", clientReferer)
					log.Printf("Referer: Passing through non-proxy client referer: %s", clientReferer)
				} else {
					log.Printf("Referer: Non-proxy client referer '%s' is not http/https. Referer removed.", clientReferer)
				}
			}
		} else {
			log.Printf("Referer: Error parsing client referer '%s': %v. Referer removed.", clientReferer, err)
		}
	} else {
		log.Println("Referer: No client referer header present. Referer removed for target.")
	}

	// Handle Origin Header:
	targetOrigin := fmt.Sprintf("%s://%s", targetURL.Scheme, targetURL.Host)
	proxyToTargetReq.Header.Set("Origin", targetOrigin)
	log.Printf("Origin header set to: %s", targetOrigin)
}


func handleProxyContent(w http.ResponseWriter, r *http.Request) {
	targetURLString := r.URL.Query().Get("url")
	if targetURLString == "" {
		http.Error(w, "Missing 'url' query parameter for proxy", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(targetURLString, "http://") && !strings.HasPrefix(targetURLString, "https://") {
		log.Printf("Warning: Target URL '%s' missing scheme, prepending http://", targetURLString)
		targetURLString = "http://" + targetURLString
	}
	targetURL, err := url.Parse(targetURLString)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.Host == "" {
		errMsg := fmt.Sprintf("Invalid target URL for proxy: '%s'. Ensure it's a complete and valid http/https URL.", targetURLString)
		if err != nil {
			errMsg += " Parsing error: " + err.Error()
		}
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	prefs := sitePreferences{
		JavaScriptEnabled:    getBoolCookie(r, "proxy-js-enabled"),
		CookiesEnabled:       getBoolCookie(r, "proxy-cookies-enabled"),
		IframesEnabled:       getBoolCookie(r, "proxy-iframes-enabled"),
		RawModeEnabled:       getBoolCookie(r, "proxy-raw-mode-enabled"), 
	}
	log.Printf("handleProxyContent: Proxying for %s. JS:%t, Cookies:%t, Iframes:%t, RawMode:%t",
		targetURL.String(), prefs.JavaScriptEnabled, prefs.CookiesEnabled, prefs.IframesEnabled, prefs.RawModeEnabled)
	
	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body) 
	if err != nil {
		http.Error(w, "Error creating target request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	setupOutgoingHeadersForProxy(proxyReq, r, targetURL, prefs)


	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse 
		},
		Timeout: 30 * time.Second, 
	}
	targetResp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Error fetching target URL %s: %v", targetURL.String(), err)
		http.Error(w, "Error fetching content from target server: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer targetResp.Body.Close()

	log.Printf("Received response from target %s: Status %s", targetURL.String(), targetResp.Status)

	originalSetCookieHeaders := targetResp.Header["Set-Cookie"] 

	for name, values := range targetResp.Header {
		lowerName := strings.ToLower(name)

		if lowerName == "set-cookie" {
			if !prefs.CookiesEnabled {
				log.Printf("Cookies disabled: Blocking Set-Cookie headers from %s", targetURL.Host)
				continue 
			}
			continue
		}

		if lowerName == "location" && (targetResp.StatusCode >= 300 && targetResp.StatusCode <= 308) {
			if len(values) > 0 {
				originalLocation := values[0]
				rewrittenLocation, err := rewriteProxiedURL(originalLocation, targetURL, r)
				if err == nil && rewrittenLocation != originalLocation {
					w.Header().Set(name, rewrittenLocation)
				} else {
					log.Printf("Error rewriting Location header '%s': %v. Passing original.", originalLocation, err)
					w.Header().Set(name, originalLocation) 
				}
			}
			continue 
		}
		if lowerName == "content-security-policy" || 
			lowerName == "content-security-policy-report-only" ||
			lowerName == "x-frame-options" || 
			lowerName == "x-xss-protection" || 
			lowerName == "strict-transport-security" || 
			lowerName == "public-key-pins" ||
			lowerName == "expect-ct" ||
			lowerName == "transfer-encoding" || 
			lowerName == "connection" ||       
			lowerName == "keep-alive" ||       
			lowerName == "content-length" {    
			continue
		}
		
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	if prefs.CookiesEnabled { 
		for _, cookieHeader := range originalSetCookieHeaders {
			w.Header().Add("Set-Cookie", cookieHeader)
		}
	}

	if targetResp.StatusCode == http.StatusNotModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	scriptNonce := generateSecureNonce() 
	
	w.Header().Set("Content-Security-Policy", generateCSP(prefs, targetURL, r, scriptNonce))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-XSS-Protection", "0") 
	w.Header().Set("Referrer-Policy", "no-referrer-when-downgrade") 
	w.Header().Set("X-Proxy-Version", "GoPrivacyProxy-v2.13-raw-mode") 

	bodyBytes, err := io.ReadAll(targetResp.Body) 
	if err != nil {
		http.Error(w, "Error reading target body: "+err.Error(), http.StatusInternalServerError)
		return
	}

	contentType := targetResp.Header.Get("Content-Type")
	isHTML := strings.HasPrefix(contentType, "text/html")
	isCSS := strings.HasPrefix(contentType, "text/css")
	
	if isHTML && prefs.RawModeEnabled {
		log.Printf("Raw Mode enabled for %s. Serving original HTML.", targetURL.String())
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes))) 
		w.WriteHeader(targetResp.StatusCode)
		w.Write(bodyBytes)
		return
	}

	isSuccess := targetResp.StatusCode >= 200 && targetResp.StatusCode < 300
	if isSuccess { 
		if isHTML {
			rewrittenHTMLReader, errRewrite := rewriteHTMLContentAdvanced(bytes.NewReader(bodyBytes), targetURL, r, prefs, scriptNonce)
			if errRewrite != nil {
				log.Printf("Error rewriting HTML for %s: %v. Serving original body.", targetURL.String(), errRewrite)
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes))) 
				w.WriteHeader(targetResp.StatusCode)
				w.Write(bodyBytes)
				return
			}
			w.WriteHeader(targetResp.StatusCode) 
			io.Copy(w, rewrittenHTMLReader)      
			return
		} else if isCSS {
			rewrittenCSS := rewriteCSSURLsInString(string(bodyBytes), targetURL, r)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(rewrittenCSS)))
			w.WriteHeader(targetResp.StatusCode)
			io.WriteString(w, rewrittenCSS)
			return
		} 
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))
	w.WriteHeader(targetResp.StatusCode)
	w.Write(bodyBytes)
}

// handleAuthCheck checks authentication and handles unauthorized responses.
// Returns true if the request should proceed, false if a response has already been sent.
func handleAuthCheck(w http.ResponseWriter, r *http.Request) bool {
	// No auth check needed for auth paths or the service worker itself.
	if strings.HasPrefix(r.URL.Path, "/auth/") || r.URL.Path == serviceWorkerPath {
		return true
	}

	isValidAuth, _, validationErr := isCFAuthCookieValid(r)
	if validationErr != nil {
		log.Printf("CF_Authorization cookie validation error for %s: %v. Auth required.", r.URL.Path, validationErr)
	}

	if !isValidAuth {
		isLikelyHTMLRequest := strings.Contains(r.Header.Get("Accept"), "text/html") ||
			r.Header.Get("Accept") == "" || r.Header.Get("Accept") == "*/*"

		// For GET requests that are likely for HTML pages (or the root), redirect to login.
		// For other requests (e.g., API calls, assets through proxy without SW), return 401.
		if r.Method == http.MethodGet && (r.URL.Path == "/" || (isLikelyHTMLRequest && r.URL.Path != proxyRequestPath)) {
			log.Printf("CF_Authorization invalid/missing for %s. Redirecting to /auth/enter-email.", r.URL.Path)
			originalURL := r.URL.RequestURI()
			http.SetCookie(w, &http.Cookie{
				Name:     "proxy-original-url",
				Value:    url.QueryEscape(originalURL),
				Path:     "/",
				HttpOnly: true,
				Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
				SameSite: http.SameSiteLaxMode,
				MaxAge:   300,
			})
			http.Redirect(w, r, "/auth/enter-email", http.StatusFound)
			return false // Response sent (redirect)
		} else {
			log.Printf("CF_Authorization invalid/missing for %s %s. Returning 401.", r.Method, r.URL.Path)
			http.Error(w, "Unauthorized: Authentication required.", http.StatusUnauthorized)
			return false // Response sent (401)
		}
	}
	return true // Auth valid, proceed
}

// handleRebasingRedirects attempts to rebase malformed or unhandled proxy-like requests
// using the "proxy-current-url" cookie.
// Returns true if a redirect was issued, false otherwise.
func handleRebasingRedirects(w http.ResponseWriter, r *http.Request) bool {
	isMalformedProxyReq := (r.URL.Path == proxyRequestPath && r.URL.Query().Get("url") == "" && r.URL.RawQuery != "")
	isServiceInfrastructurePath := r.URL.Path == "/" || r.URL.Path == proxyRequestPath || r.URL.Path == serviceWorkerPath || strings.HasPrefix(r.URL.Path, "/auth/")
	isUnsupportedPath := !isServiceInfrastructurePath

	if !isMalformedProxyReq && !isUnsupportedPath {
		return false // Not a candidate for rebasing
	}

	currentURLCookie, errCookie := r.Cookie("proxy-current-url")
	if errCookie != nil || currentURLCookie.Value == "" {
		if isUnsupportedPath || isMalformedProxyReq {
			log.Printf("Rebasing: proxy-current-url cookie not found or empty. Cannot rebase %s", r.URL.String())
		}
		return false 
	}

	log.Printf("Rebasing: Attempting rebase for %s using proxy-current-url cookie (value assumed to be unencoded target URL): %s", r.URL.String(), currentURLCookie.Value)
	
	// Assume cookie value is the direct unencoded target URL
	baseTargetString := currentURLCookie.Value

	baseTargetURL, errParseBaseTarget := url.Parse(baseTargetString)
	if errParseBaseTarget != nil || !baseTargetURL.IsAbs() {
		log.Printf("Rebasing Error: Could not parse baseTargetURL from cookie ('%s') or not absolute: %v", baseTargetString, errParseBaseTarget)
		return false
	}

	var rebasedTargetURL *url.URL
	if isUnsupportedPath { // e.g. /some/other/path.css?p=1
		// Resolve current request path and query against the baseTargetURL
		rebasedTargetURL = baseTargetURL.ResolveReference(r.URL)
		log.Printf("Rebasing unsupported path: %s against %s -> %s", r.URL.String(), baseTargetURL.String(), rebasedTargetURL.String())
	} else { // isMalformedProxyReq (e.g. /proxy?param=val, missing url)
		rebasedTargetURL = new(url.URL)
		*rebasedTargetURL = *baseTargetURL // Copy base (scheme, host, path from original target)
		
		newQuery := baseTargetURL.Query() 
		for key, values := range r.URL.Query() { 
			newQuery[key] = values
		}
		rebasedTargetURL.RawQuery = newQuery.Encode()
		log.Printf("Rebasing malformed proxy: %s with query %s onto %s -> %s", r.URL.Path, r.URL.RawQuery, baseTargetURL.String(), rebasedTargetURL.String())
	}

	finalProxyRedirectURLString := fmt.Sprintf("%s?url=%s", proxyRequestPath, url.QueryEscape(rebasedTargetURL.String()))
	log.Printf("Rebasing: Redirecting client to: %s", finalProxyRedirectURLString)
	http.Redirect(w, r, finalProxyRedirectURLString, http.StatusFound)
	return true // Redirect was issued
}


// --- Master Handler (Auth Gatekeeper & Router) ---
func masterHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("masterHandler: Path %s, Method: %s", r.URL.Path, r.Method)

	// Perform authentication check. If it returns false, a response has already been sent.
	if !handleAuthCheck(w, r) {
		return
	}

	// Attempt rebasing for malformed or unhandled proxy-like requests.
	// If a redirect is issued, handleRebasingRedirects returns true and we should stop further processing.
	if handleRebasingRedirects(w,r) {
		return
	}

	// Routing logic
	switch r.URL.Path {
	case "/":
		handleLandingPage(w, r)
	case proxyRequestPath:
		handleProxyContent(w, r)
	case serviceWorkerPath:
		serveServiceWorkerJS(w, r)
	default:
		http.NotFound(w, r)
	}
}


// --- Utility functions from original auth flow (logging, passthrough) ---
func passThroughResponse(w http.ResponseWriter, clientRequestHost string, sourceResp *http.Response, bodyBytes []byte, originalSetCookieHeaders []string, wasDecompressed bool) {
	log.Printf("Auth Passthrough: Relaying response from %s (Status: %s)", sourceResp.Request.URL.String(), sourceResp.Status)
	for name, values := range sourceResp.Header {
		lowerName := strings.ToLower(name)
		if (lowerName == "content-encoding" && wasDecompressed) ||
		   (lowerName == "content-length" && wasDecompressed) ||
		   lowerName == "transfer-encoding" || 
		   lowerName == "connection" {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	for _, cookieHeader := range originalSetCookieHeaders {
		w.Header().Add("Set-Cookie", cookieHeader)
	}

	if wasDecompressed { 
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))
	}

	w.WriteHeader(sourceResp.StatusCode)
	_, err := w.Write(bodyBytes)
	if err != nil {
		log.Printf("Error writing passthrough response body to client: %v", err)
	}
}

func logReasonsForNotAutomating(isHTML bool, statusCode int, hasAuthCookie bool, method string) { /* ... */ }
func determineClientRedirectPath(cfLocation string) string { /* ... */ return cfLocation }
func logEmailPostRequest(req *http.Request, formData string) { /* ... */ }
func logEmailPostResponse(resp *http.Response) { /* ... */ }
func logCodeSubmitRequest(req *http.Request, formData string) { /* ... */ }
func logCodeSubmitResponse(resp *http.Response) { /* ... */ }

/*
Example app.yaml for Google App Engine Standard:

runtime: go122 

handlers:
- url: /.*
  script: auto
  secure: always 

env_variables:
  AUTH_SERVICE_URL: "YOUR_CLOUDFLARE_ACCESS_PROTECTED_URL_HERE" 
*/
