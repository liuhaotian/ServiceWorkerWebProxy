package main

import (
	"bytes"
	"compress/gzip"
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
	// Default privacy settings (can be overridden by user preferences later)
	// These are used if no specific preference cookies are found.
	defaultGlobalJSEnabled      = false
	defaultGlobalCookiesEnabled = false
	defaultGlobalIframesEnabled = false

	authServiceURL string
)

// Cookie names & Constants
const (
	authCookieName        = "CF_Authorization" // Cookie for this proxy's own auth
	maxRedirects          = 5                  // Max redirects for the proxy to follow internally
	originalURLCookieName = "proxy_original_url"
	proxyRequestPath      = "/proxy"
	// staticAssetBasePath is no longer needed for serving, but SW might reference it.
	// staticAssetBasePath   = "/static/" 
	defaultUserAgent      = "PrivacyProxy/1.0 (Appspot; +https://github.com/your-repo/privacy-proxy)"
)

// Regex for parsing forms (used in auth flow)
var (
	formActionRegex    = regexp.MustCompile(`(?is)<form[^>]*action\s*=\s*["']([^"']+)["'][^>]*>`)
	hiddenInputRegex   = regexp.MustCompile(`(?is)<input[^>]*type\s*=\s*["']hidden["'][^>]*name\s*=\s*["']([^"']+)["'][^>]*value\s*=\s*["']([^"']*)["'][^>]*>`)
	nonceInputRegex    = regexp.MustCompile(`(?is)<input[^>]*name\s*=\s*["']nonce["'][^>]*value\s*=\s*["']([^"']+)["']`)
	codeInputFormRegex = regexp.MustCompile(`(?is)<form[^>]*action\s*=\s*["']([^"']*/cdn-cgi/access/callback[^"']*)["'][^>]*>`) // For CF's code page
	// Updated cssURLRegex: Removed negative lookahead. Check for "data:" will be done in the callback.
	cssURLRegex        = regexp.MustCompile(`(?i)url\s*\(\s*(?:'([^']*)'|"([^"]*)"|([^)\s'"]+))\s*\)`)
)

// sitePreferences holds the privacy settings for a site.
type sitePreferences struct {
	JavaScriptEnabled bool
	CookiesEnabled    bool
	IframesEnabled    bool
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
    margin: 0; 
    background-color: #f0f2f5; 
    color: #1f2937; 
    line-height: 1.6;
    display: flex;
    flex-direction: column;
    min-height: 100vh;
}
.container { 
    flex: 1;
    max-width: 800px; 
    margin: 20px auto; 
    padding: 25px; 
    background-color: #ffffff; 
    border-radius: 12px; 
    box-shadow: 0 6px 12px rgba(0,0,0,0.08); 
}
header { 
    text-align: center; 
    margin-bottom: 30px; 
    padding-bottom: 20px; 
    border-bottom: 1px solid #e5e7eb; 
}
header h1 { 
    color: #1e40af; /* Tailwind blue-800 */
    font-size: 2em;
    margin-bottom: 0.25em;
}
header p {
    color: #4b5563; /* Tailwind gray-600 */
    font-size: 1.1em;
}
section { 
    margin-bottom: 30px; 
    padding: 20px; 
    border: 1px solid #d1d5db; /* Tailwind gray-300 */
    border-radius: 8px; 
    background-color: #f9fafb; /* Tailwind gray-50 */
}
h2 { 
    color: #1e3a8a; /* Tailwind blue-700 */
    margin-top: 0; 
    padding-bottom: 10px; 
    font-size: 1.5em;
    border-bottom: 2px solid #bfdbfe; /* Tailwind blue-200 */
}
input[type="url"], input[type="text"] { 
    width: calc(100% - 24px); 
    padding: 12px; 
    margin-bottom: 12px; 
    border: 1px solid #9ca3af; /* Tailwind gray-400 */
    border-radius: 6px; 
    font-size: 1em;
    transition: border-color 0.2s ease-in-out, box-shadow 0.2s ease-in-out;
}
input[type="url"]:focus, input[type="text"]:focus {
    border-color: #2563eb; /* Tailwind blue-600 */
    box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.3); /* Tailwind blue-500 with opacity */
    outline: none;
}
button, input[type="checkbox"] {
    cursor: pointer;
}
button { 
    padding: 10px 18px; 
    background-color: #2563eb; /* Tailwind blue-600 */
    color: white; 
    border: none; 
    border-radius: 6px; 
    font-size: 1em;
    font-weight: 500;
    transition: background-color 0.2s ease-in-out;
    margin-right: 8px;
}
button:hover { 
    background-color: #1d4ed8; /* Tailwind blue-700 */
}
button[type="button"], .cancel-btn { /* Specific styling for cancel/secondary buttons */
    background-color: #6b7280; /* Tailwind gray-500 */
}
button[type="button"]:hover, .cancel-btn:hover {
    background-color: #4b5563; /* Tailwind gray-600 */
}
.setting-item, .bookmark-item-details { 
    margin-bottom: 12px; 
    padding: 10px; 
    border-radius: 4px; 
    background-color: #f3f4f6; /* Tailwind gray-100 */
    display: flex;
    align-items: center;
}
.setting-item label, .bookmark-item-details label { 
    margin-right: 10px; 
    color: #374151; /* Tailwind gray-700 */
    flex-grow: 1;
}
input[type="checkbox"] {
    margin-left: auto; /* Pushes checkbox to the right */
    transform: scale(1.2);
}
.setting-item-inline {
    display: inline-block;
    margin-right: 15px;
    margin-bottom: 10px;
}
.setting-item-inline label {
    font-size: 0.9em;
}
#bookmarks-list .bookmark-item { 
    display: flex; 
    justify-content: space-between; 
    align-items: center; 
    padding: 12px;
    border-bottom: 1px solid #e5e7eb; /* Tailwind gray-200 */
}
#bookmarks-list .bookmark-item:last-child {
    border-bottom: none;
}
#bookmarks-list .bookmark-info {
    flex-grow: 1;
}
#bookmarks-list .bookmark-item a.go-bookmark-link { 
    color: #1e40af; /* Tailwind blue-800 */
    text-decoration: none; 
    font-weight: 500;
}
#bookmarks-list .bookmark-item a.go-bookmark-link:hover { 
    text-decoration: underline; 
    color: #1d4ed8; /* Tailwind blue-700 */
}
#bookmarks-list .bookmark-item .actions button { 
    font-size: 0.85em; 
    padding: 6px 10px; 
    margin-left: 8px;
}
#bookmark-form-container {
    margin-top: 20px;
    padding: 15px;
    border: 1px dashed #9ca3af; /* Tailwind gray-400 */
    border-radius: 6px;
}
#bookmark-form-container label {
    display: block;
    margin-bottom: 5px;
    font-weight: 500;
    color: #374151;
}
.error-message {
    color: #dc2626; /* Tailwind red-600 */
    background-color: #fee2e2; /* Tailwind red-100 */
    border: 1px solid #fca5a5; /* Tailwind red-300 */
    padding: 10px;
    border-radius: 6px;
    margin-top: 10px;
    display: none; /* Hidden by default */
}
footer {
    text-align: center;
    padding: 15px;
    background-color: #1f2937; /* Tailwind gray-800 */
    color: #d1d5db; /* Tailwind gray-300 */
    font-size: 0.9em;
    margin-top: auto;
}
`

const combinedInjectedHTML = `
<style id="proxy-home-button-styles" type="text/css">
#proxy-home-button {
    position: fixed !important;
    bottom: 20px !important;
    right: 20px !important;
    width: 48px !important; /* Adjusted for round button */
    height: 48px !important; /* Adjusted for round button */
    padding: 0 !important; /* Remove padding if icon fills space */
    background-color: rgba(0, 123, 255, 0.85) !important;
    color: white !important;
    text-decoration: none !important;
    border-radius: 50% !important; /* Make it round */
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
    background-color: rgba(0, 100, 220, 1) !important;
    transform: scale(1.1) !important; 
}
#proxy-home-button svg {
    width: 24px !important; /* Adjust icon size as needed */
    height: 24px !important;
    fill: currentColor !important;
}
</style>
<a href="/" id="proxy-home-button" title="Return to Proxy Home">
    <svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z"/></svg>
</a>
`

const clientJSContentForEmbedding = `
// --- Start of clientJSContentForEmbedding ---
    // This entire block will be embedded in a <script> tag in the HTML.
    // It includes the Service Worker code as a string, then the client logic.

    const embeddedSWContent = ` + "`" + `
// --- Start of embeddedSWContent (Service Worker Code) ---
const PROXY_ENDPOINT = '/proxy'; // The Go backend's proxy handler path
const STATIC_ASSET_PATH_PREFIX = '/static/'; // Prefix for the proxy's own static assets (now less relevant)

self.addEventListener('install', event => {
    console.log('Service Worker: Installing...');
    event.waitUntil(self.skipWaiting()); // Activate worker immediately
});

self.addEventListener('activate', event => {
    console.log('Service Worker: Activating...');
    event.waitUntil(self.clients.claim()); // Take control of all open clients
});

self.addEventListener('fetch', event => {
    const request = event.request;
    const requestUrl = new URL(request.url);

    // --- Conditions to bypass Service Worker proxying logic ---
    if (requestUrl.origin === self.origin && requestUrl.pathname.startsWith(STATIC_ASSET_PATH_PREFIX)) {
        event.respondWith(fetch(request));
        return;
    }
    if (requestUrl.origin === self.origin && requestUrl.pathname.startsWith('/auth/')) {
        event.respondWith(fetch(request));
        return;
    }
    if (requestUrl.origin === self.origin && requestUrl.pathname === PROXY_ENDPOINT && requestUrl.searchParams.has('url')) {
        event.respondWith(fetch(request));
        return;
    }
    if (!requestUrl.protocol.startsWith('http')) {
        event.respondWith(fetch(request));
        return;
    }
    
    // --- Main Proxying Logic ---
    event.respondWith(async function() {
        try {
            const client = await self.clients.get(event.clientId);
            if (!client || !client.url) {
                return fetch(request); 
            }

            const clientPageUrl = new URL(client.url); 

            if (!(clientPageUrl.origin === self.origin && clientPageUrl.pathname === PROXY_ENDPOINT && clientPageUrl.searchParams.has('url'))) {
                return fetch(request); 
            }

            const originalProxiedBaseUrlString = clientPageUrl.searchParams.get('url');
            if (!originalProxiedBaseUrlString) {
                console.error('SW: Could not get original proxied base URL from client:', client.url);
                return fetch(request); 
            }

            let finalTargetUrlString;
            if (requestUrl.origin === self.origin) {
                try {
                    const baseForResolution = new URL(originalProxiedBaseUrlString);
                    finalTargetUrlString = new URL(requestUrl.pathname + requestUrl.search + requestUrl.hash, baseForResolution).toString();
                } catch (e) {
                    // Use string concatenation instead of JS template literal for console.error
                    console.error('SW: Error resolving same-origin relative URL \'' + request.url + '\' against base \'' + originalProxiedBaseUrlString + '\':', e);
                    // Use string concatenation instead of JS template literal for new Response
                    return new Response('Error resolving URL: ' + e.message, { status: 500 });
                }
            } else {
                finalTargetUrlString = request.url;
            }
            
            const newProxyRequestUrl = new URL(PROXY_ENDPOINT, self.location.origin);
            newProxyRequestUrl.searchParams.set('url', finalTargetUrlString);
            
            const newHeaders = new Headers(request.headers);
            newHeaders.delete('Range'); 
            newHeaders.delete('If-Range');

            return fetch(newProxyRequestUrl.toString(), {
                method: request.method,
                headers: newHeaders,
                body: (request.method === 'GET' || request.method === 'HEAD') ? undefined : await request.blob(),
                mode: 'cors', 
                credentials: 'omit', 
                redirect: 'manual',  
            });

        } catch (error) {
            console.error('SW Fetch Error:', error, 'For request:', request.url);
            // Use string concatenation instead of JS template literal for new Response
            return new Response('Service Worker fetch processing error: ' + error.message, { status: 500, headers: { 'Content-Type': 'text/plain'} });
        }
    }());
});
// --- End of embeddedSWContent ---
` + "`" + `; // End of embeddedSWContent string literal

    // --- Start of Client Logic (formerly client.js) ---
    document.addEventListener('DOMContentLoaded', () => {
        if ('serviceWorker' in navigator) {
            try {
                const swBlob = new Blob([embeddedSWContent], { type: 'application/javascript' });
                const swUrl = URL.createObjectURL(swBlob);
                navigator.serviceWorker.register(swUrl, { scope: '/' })
                    .then(registration => {
                        console.log('Service Worker registered with scope:', registration.scope);
                        registration.update(); 
                    })
                    .catch(error => console.error('Service Worker registration failed:', error));
            } catch (e) {
                console.error('Error creating/registering SW from blob:', e);
            }
            
            navigator.serviceWorker.oncontrollerchange = () => {
                console.log('New Service Worker activated.');
            };
        }

        const proxyForm = document.getElementById('proxy-form');
        const urlInput = document.getElementById('url-input');
        const errorMessageDiv = document.getElementById('error-message');
        const globalJsCheckbox = document.getElementById('global-js');
        const globalCookiesCheckbox = document.getElementById('global-cookies');
        const globalIframesCheckbox = document.getElementById('global-iframes');
        const bookmarksList = document.getElementById('bookmarks-list');
        const showAddBookmarkFormBtn = document.getElementById('show-add-bookmark-form-btn');
        const bookmarkCurrentSiteBtn = document.getElementById('bookmark-current-site-btn');
        const bookmarkFormContainer = document.getElementById('bookmark-form-container');
        const bookmarkFormTitle = document.getElementById('bookmark-form-title');
        const bookmarkEditIndexInput = document.getElementById('bookmark-edit-index');
        const bookmarkNameInput = document.getElementById('bookmark-name');
        const bookmarkUrlInput = document.getElementById('bookmark-url');
        const bookmarkJsCheckbox = document.getElementById('bookmark-js');
        const bookmarkCookiesCheckbox = document.getElementById('bookmark-cookies');
        const bookmarkIframesCheckbox = document.getElementById('bookmark-iframes');
        const saveBookmarkBtn = document.getElementById('save-bookmark-btn');
        const cancelBookmarkBtn = document.getElementById('cancel-bookmark-btn');

        const settingsKeys = { 
            js: 'proxy-js-enabled', 
            cookies: 'proxy-cookies-enabled', 
            iframes: 'proxy-iframes-enabled' 
        };
        
        function loadGlobalSettings() {
            globalJsCheckbox.checked = localStorage.getItem(settingsKeys.js) === 'true';
            globalCookiesCheckbox.checked = localStorage.getItem(settingsKeys.cookies) === 'true';
            globalIframesCheckbox.checked = localStorage.getItem(settingsKeys.iframes) === 'true';
            updatePreferenceCookies(getGlobalSettings()); 
        }

        function getGlobalSettings() {
            return {
                js: globalJsCheckbox.checked,
                cookies: globalCookiesCheckbox.checked,
                iframes: globalIframesCheckbox.checked
            };
        }

        function saveGlobalSettings() {
            const settings = getGlobalSettings();
            localStorage.setItem(settingsKeys.js, settings.js);
            localStorage.setItem(settingsKeys.cookies, settings.cookies);
            localStorage.setItem(settingsKeys.iframes, settings.iframes);
            updatePreferenceCookies(settings); 
        }

        function updatePreferenceCookies(prefs) { 
            const cookieOptions = 'path=/; SameSite=Lax; max-age=31536000'; 
            document.cookie = 'proxy-js-enabled=' + prefs.js + '; ' + cookieOptions; 
            document.cookie = 'proxy-cookies-enabled=' + prefs.cookies + '; ' + cookieOptions; 
            document.cookie = 'proxy-iframes-enabled=' + prefs.iframes + '; ' + cookieOptions; 
        }

        globalJsCheckbox.addEventListener('change', saveGlobalSettings);
        globalCookiesCheckbox.addEventListener('change', saveGlobalSettings);
        globalIframesCheckbox.addEventListener('change', saveGlobalSettings);

        proxyForm.addEventListener('submit', (event) => {
            errorMessageDiv.style.display = 'none';
            errorMessageDiv.textContent = '';
            const targetUrl = urlInput.value.trim();
            if (!targetUrl) {
                event.preventDefault(); 
                showError("URL cannot be empty.");
                return;
            }
            if (!isValidHttpUrl(targetUrl)) {
                event.preventDefault(); 
                showError("Please enter a valid URL (e.g., http://example.com or https://example.com).");
                return;
            }
            saveGlobalSettings(); 
        });

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

        const BOOKMARKS_LS_KEY = 'proxy-bookmarks-v2';

        function loadBookmarks() {
            const bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            bookmarksList.innerHTML = ''; 
            if (bookmarks.length === 0) {
                bookmarksList.innerHTML = '<p>No bookmarks yet. Add one or bookmark a visited site!</p>';
                return;
            }
            bookmarks.forEach((bm, index) => {
                const item = document.createElement('div');
                item.className = 'bookmark-item';
                // Convert JS template literal to string concatenation
                item.innerHTML = 
                    '<div class="bookmark-info">' +
                        '<a href="#" class="go-bookmark-link" data-url="' + escapeHTML(bm.url) + '" data-prefs=\'' + JSON.stringify(bm.prefs) + '\'>' + escapeHTML(bm.name) + '</a>' +
                        '<small style="display:block; color:#6b7280;">' + escapeHTML(bm.url) + '</small>' +
                        '<small style="display:block; color:#6b7280; font-size:0.8em;">' +
                            'Stored Prefs: JS(' + (bm.prefs.js ? '(Y)':'(N)') + ') Cookies(' + (bm.prefs.cookies ? '(Y)':'(N)') + ') iFrames(' + (bm.prefs.iframes ? '(Y)':'(N)') + ')' +
                        '</small>' +
                    '</div>' +
                    '<div class="actions">' +
                        '<button data-index="' + index + '" class="edit-bookmark">Edit</button>' +
                        '<button data-index="' + index + '" class="delete-bookmark">Del</button>' +
                    '</div>';
                bookmarksList.appendChild(item);
            });

            document.querySelectorAll('.go-bookmark-link').forEach(link => {
                link.addEventListener('click', function(e) {
                    e.preventDefault();
                    const url = this.dataset.url;
                    updatePreferenceCookies(getGlobalSettings()); 
                    window.location.href = '/proxy?url=' + encodeURIComponent(url);
                });
            });
            
            document.querySelectorAll('.edit-bookmark').forEach(button => {
                button.addEventListener('click', function() {
                    openBookmarkFormForEdit(parseInt(this.dataset.index));
                });
            });

            document.querySelectorAll('.delete-bookmark').forEach(button => {
                button.addEventListener('click', function() {
                    if(confirm('Are you sure you want to delete this bookmark?')) {
                        deleteBookmark(parseInt(this.dataset.index));
                    }
                });
            });
        }

        function openBookmarkFormForAdd() {
            bookmarkFormTitle.textContent = 'Add';
            bookmarkEditIndexInput.value = ''; 
            bookmarkNameInput.value = '';
            bookmarkUrlInput.value = '';
            const globalPrefs = getGlobalSettings(); 
            bookmarkJsCheckbox.checked = globalPrefs.js;
            bookmarkCookiesCheckbox.checked = globalPrefs.cookies;
            bookmarkIframesCheckbox.checked = globalPrefs.iframes;
            bookmarkFormContainer.style.display = 'block';
            bookmarkNameInput.focus();
        }

        function openBookmarkFormForEdit(index) {
            const bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            const bm = bookmarks[index];
            if (!bm) return;
            bookmarkFormTitle.textContent = 'Edit';
            bookmarkEditIndexInput.value = index;
            bookmarkNameInput.value = bm.name;
            bookmarkUrlInput.value = bm.url;
            bookmarkJsCheckbox.checked = bm.prefs.js;
            bookmarkCookiesCheckbox.checked = bm.prefs.cookies;
            bookmarkIframesCheckbox.checked = bm.prefs.iframes;
            bookmarkFormContainer.style.display = 'block';
            bookmarkNameInput.focus();
        }

        showAddBookmarkFormBtn.addEventListener('click', openBookmarkFormForAdd);

        cancelBookmarkBtn.addEventListener('click', () => {
            bookmarkFormContainer.style.display = 'none';
        });

        saveBookmarkBtn.addEventListener('click', () => {
            const name = bookmarkNameInput.value.trim();
            const urlValue = bookmarkUrlInput.value.trim(); 
            const editIndex = bookmarkEditIndexInput.value;

            if (!name || !urlValue) {
                alert("Bookmark name and URL are required.");
                return;
            }
            if (!isValidHttpUrl(urlValue)) {
                alert("Please enter a valid URL for the bookmark (e.g., http://example.com).");
                return;
            }
            const prefs = { 
                js: bookmarkJsCheckbox.checked,
                cookies: bookmarkCookiesCheckbox.checked,
                iframes: bookmarkIframesCheckbox.checked,
            };
            const bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            const newBookmark = { name, url: urlValue, prefs };

            if (editIndex !== '') { 
                bookmarks[parseInt(editIndex)] = newBookmark;
            } else { 
                if (bookmarks.some(bm => bm.url === urlValue)) {
                    console.log("Note: A bookmark with this URL already exists.");
                }
                bookmarks.push(newBookmark); 
            }
            localStorage.setItem(BOOKMARKS_LS_KEY, JSON.stringify(bookmarks));
            loadBookmarks();
            bookmarkFormContainer.style.display = 'none';
        });
        
        function deleteBookmark(index) {
            const bookmarks = JSON.parse(localStorage.getItem(BOOKMARKS_LS_KEY)) || [];
            bookmarks.splice(index, 1);
            localStorage.setItem(BOOKMARKS_LS_KEY, JSON.stringify(bookmarks));
            loadBookmarks();
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
                    openBookmarkFormForAdd(); 
                    bookmarkUrlInput.value = currentUrl;
                    try {
                        bookmarkNameInput.value = new URL(currentUrl).hostname;
                    } catch (e) {
                        bookmarkNameInput.value = "My Site";
                    }
                };
            }
        } else {
             if(bookmarkCurrentSiteBtn) bookmarkCurrentSiteBtn.style.display = 'none';
        }
        
        function getCookieValue(name) {
            const nameEQ = name + "=";
            const ca = document.cookie.split(';');
            for(let i = 0; i < ca.length; i++) {
                let c = ca[i];
                while (c.charAt(0) === ' ') c = c.substring(1, c.length);
                if (c.indexOf(nameEQ) === 0) return c.substring(nameEQ.length, c.length);
            }
            return null;
        }

        loadGlobalSettings();
        loadBookmarks();
    }); // End of DOMContentLoaded listener
// --- End of Client Logic ---
`

// landingPageHTMLContent is now constructed in makeLandingPageHTML()
// to include the dynamic CSS and JS content.

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

// makeLandingPageHTML constructs the full HTML for the landing page, embedding CSS and JS.
func makeLandingPageHTML() string {
	// Base HTML structure (from original landingPageHTMLContent, but with placeholders for CSS/JS)
	baseHTML := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Privacy Web Proxy</title>
    <style type="text/css">
%s
    </style>
    <meta http-equiv="Content-Security-Policy" 
          content="default-src 'self' blob:; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; object-src 'none'; base-uri 'self'; form-action 'self';">
</head>
<body>
    <div class="container">
        <header>
            <h1>Privacy Web Proxy 🛡️</h1>
            <p>Browse the web with enhanced privacy controls.</p>
        </header>

        <section class="url-input-section">
            <h2>Enter URL to Proxy:</h2>
            <form id="proxy-form" action="/proxy" method="GET">
                <input type="url" id="url-input" name="url" placeholder="https://example.com" required>
                <button type="submit">Go</button>
            </form>
            <div id="error-message" class="error-message"></div>
        </section>

        <section class="settings-section">
            <h2>Global Default Settings:</h2>
            <div class="setting-item">
                <label for="global-js">Enable JavaScript:</label>
                <input type="checkbox" id="global-js">
            </div>
            <div class="setting-item">
                <label for="global-cookies">Allow Cookies (from target site):</label>
                <input type="checkbox" id="global-cookies">
            </div>
            <div class="setting-item">
                <label for="global-iframes">Allow iFrames:</label>
                <input type="checkbox" id="global-iframes">
            </div>
        </section>

        <section class="bookmarks-section">
            <h2>Bookmarks:</h2>
            <div id="bookmarks-list">
                </div>
            <button id="show-add-bookmark-form-btn">Add New Bookmark</button>
            <button id="bookmark-current-site-btn" style="display:none;">Bookmark Current Site</button>
            
            <div id="bookmark-form-container" style="display:none;">
                <h3><span id="bookmark-form-title">Add</span> Bookmark</h3>
                <input type="hidden" id="bookmark-edit-index">
                <label for="bookmark-name">Name:</label>
                <input type="text" id="bookmark-name" placeholder="Bookmark Name (e.g., Site Name)" required>
                <label for="bookmark-url">URL:</label>
                <input type="url" id="bookmark-url" placeholder="Site URL (will be proxied)" required>
                <div class="setting-item-inline"><label><input type="checkbox" id="bookmark-js"> JS</label></div>
                <div class="setting-item-inline"><label><input type="checkbox" id="bookmark-cookies"> Cookies</label></div>
                <div class="setting-item-inline"><label><input type="checkbox" id="bookmark-iframes"> iFrames</label></div>
                <button id="save-bookmark-btn">Save Bookmark</button>
                <button id="cancel-bookmark-btn" type="button">Cancel</button>
            </div>
        </section>
    </div>
    <footer>
        <p>Privacy Proxy. Use with caution.</p>
    </footer>
    <script type="text/javascript">
//<![CDATA[
%s
//]]>
    </script>
</body>
</html>
`
	// Note: CSP updated to allow blob: for SW and 'unsafe-eval' for client.js (if any part uses it, safer to allow for now).
	// Consider refining CSP if 'unsafe-eval' is not strictly needed by client.js.
	return fmt.Sprintf(baseHTML, styleCSSContent, clientJSContentForEmbedding)
}


func main() {
	initEnv()

	// Auth flow handlers
	http.HandleFunc("/auth/enter-email", handleServeEmailPage)
	http.HandleFunc("/auth/submit-email", handleSubmitEmailToExternalCF)
	http.HandleFunc("/auth/submit-code", handleSubmitCodeToExternalCF)

	// Master handler gates access to landing page and proxy functionality
	http.HandleFunc("/", masterHandler)

	log.Printf("Starting privacy-centric proxy server with auth on port %s", listenPort)
	if err := http.ListenAndServe(":"+listenPort, nil); err != nil {
		log.Fatalf("ListenAndServe error: %v", err)
	}
}

// --- Static Asset Handlers (REMOVED as assets are embedded) ---
// func serveStyleCSS(w http.ResponseWriter, r *http.Request) { ... }
// func serveClientJS(w http.ResponseWriter, r *http.Request) { ... }
// func serveServiceWorkerJS(w http.ResponseWriter, r *http.Request) { ... }


// --- Utility Helper Functions ---
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isCFAuthCookieValid(r *http.Request) (isValid bool, payload *JWTPayload, err error) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false, nil, nil // No cookie, not an error per se, just not valid
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
	if origURLCookie, err := r.Cookie(originalURLCookieName); err == nil {
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
		http.SetCookie(w, &http.Cookie{Name: originalURLCookieName, Value: url.QueryEscape(originalURLPath), Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode, MaxAge: 300})
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
		if cookie.Name != originalURLCookieName && cookie.Name != authCookieName {
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
		http.SetCookie(w, &http.Cookie{Name: originalURLCookieName, Value: "", Path: "/", MaxAge: -1})

		var body strings.Builder
		body.WriteString("<h1>Proxy Authentication Successful!</h1><p>You can now use the proxy service.</p>")
		if decodedJWTPayload != nil {
			body.WriteString("<h2>Decoded JWT Payload (from external CF):</h2><pre>")
			payloadBytes, _ := json.MarshalIndent(decodedJWTPayload, "", "  ")
			body.WriteString(stdhtml.EscapeString(string(payloadBytes)))
			body.WriteString("</pre>")
		}
		originalURLPath := "/" 
		if origURLCookie, errCookie := r.Cookie(originalURLCookieName); errCookie == nil {
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

// getBoolCookie checks for a cookie and returns true if it exists and its value is "true".
// Returns false otherwise (not found, error, or different value).
func getBoolCookie(r *http.Request, name string) bool {
	cookie, err := r.Cookie(name)
	if err != nil {
		return false // Cookie not found or other error
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

func rewriteHTMLContentAdvanced(htmlReader io.Reader, pageBaseURL *url.URL, clientReq *http.Request, prefs sitePreferences) (io.Reader, error) {
	doc, err := html.Parse(htmlReader)
	if err != nil {
		return nil, fmt.Errorf("HTML parsing error: %w", err)
	}

	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "script" {
				if !prefs.JavaScriptEnabled {
					n.Attr = []html.Attribute{{Key: "type", Val: "text/inert-script"}} 
					for c := n.FirstChild; c != nil; { 
						next := c.NextSibling
						n.RemoveChild(c)
						c = next
					}
				} else { 
					for i, attr := range n.Attr {
						if strings.ToLower(attr.Key) == "src" && attr.Val != "" {
							if proxiedURL, err := rewriteProxiedURL(attr.Val, pageBaseURL, clientReq); err == nil && proxiedURL != attr.Val {
								n.Attr[i].Val = proxiedURL
							}
						}
					}
				}
			} else if n.Data == "iframe" || n.Data == "frame" { 
				if !prefs.IframesEnabled {
					newAttrs := []html.Attribute{{Key: "src", Val: "about:blank"}}
					n.Attr = newAttrs
				} else { 
					for i, attr := range n.Attr {
						if strings.ToLower(attr.Key) == "src" && attr.Val != "" {
							if proxiedURL, err := rewriteProxiedURL(attr.Val, pageBaseURL, clientReq); err == nil && proxiedURL != attr.Val {
								n.Attr[i].Val = proxiedURL
							}
						}
					}
				}
			} else { 
				var newAttrs []html.Attribute
				for _, attr := range n.Attr {
					currentAttr := attr // Keep a copy of the attribute being processed
					attrKeyLower := strings.ToLower(currentAttr.Key)
					attrVal := strings.TrimSpace(currentAttr.Val)
					
					isHomeButtonLink := false
					if n.Data == "a" && attrKeyLower == "href" && attrVal == "/" {
						// Check if this 'a' tag is our specific home button by its ID
						isOurButton := false
						for _, a := range n.Attr { 
							if strings.ToLower(a.Key) == "id" && a.Val == "proxy-home-button" {
								isOurButton = true
								break
							}
						}
						if isOurButton {
							isHomeButtonLink = true
						}
					}


					if isHomeButtonLink {
						log.Printf("DEBUG: Home button href='/', preserving as is.")
						// currentAttr.Val is already "/", so no change needed.
						// It will be added to newAttrs as is.
					} else { // Apply rewriting logic for all other attributes
						switch attrKeyLower {
						case "href", "src", "action", "longdesc", "cite", "formaction", "icon", "manifest", "poster", "data", "background":
							if attrVal != "" {
								if proxiedURL, err := rewriteProxiedURL(attrVal, pageBaseURL, clientReq); err == nil && proxiedURL != attrVal {
									currentAttr.Val = proxiedURL
								} else if err != nil {
									log.Printf("HTML Rewrite: Error proxying URL for attr '%s' val '%s' (base '%s'): %v", attrKeyLower, attrVal, pageBaseURL.String(), err)
								}
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
							continue // Skip adding this attribute
						}
					}


					if strings.HasPrefix(attrKeyLower, "on") && !prefs.JavaScriptEnabled {
						continue 
					}
					
					newAttrs = append(newAttrs, currentAttr) // Add the (potentially modified) attribute
				}
				n.Attr = newAttrs
			}
		}

		// Inject combined CSS and button HTML into <body>
		if n.Type == html.ElementNode && n.Data == "body" {
			log.Println("DEBUG: Processing <body> tag for combined button/style injection.")
			
			alreadyExists := false
			// Check if either the button or its style is already present
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					if c.Data == "a" {
						for _, attr := range c.Attr {
							if attr.Key == "id" && attr.Val == "proxy-home-button" {
								alreadyExists = true; break
							}
						}
					} else if c.Data == "style" {
						for _, attr := range c.Attr {
							if attr.Key == "id" && attr.Val == "proxy-home-button-styles" {
								alreadyExists = true; break
							}
						}
					}
				}
				if alreadyExists { break }
			}
			log.Printf("DEBUG: Combined button/style 'alreadyExists' check result: %t", alreadyExists)

			if !alreadyExists {
				// Parse the combined HTML. Using nil context parses it as if it's content of <body>.
				parsedNodes, errFrag := html.ParseFragment(strings.NewReader(combinedInjectedHTML), nil) 
				
				if errFrag != nil {
					log.Printf("ERROR parsing HTML fragment for combined button/style: %v. HTML: %s", errFrag, combinedInjectedHTML)
				} else if len(parsedNodes) == 0 {
					log.Println("DEBUG: ParsedNodes for combined button/style is empty.")
				} else {
					for _, nodeToAdd := range parsedNodes { 
						log.Printf("DEBUG: Attempting to append combined node type %d, data '%s' to body", nodeToAdd.Type, nodeToAdd.Data)
						n.AppendChild(nodeToAdd) 
					}
					log.Println("Successfully appended combined button/style to <body>.")
				}
			} else {
				log.Println("Combined button/style already exists in <body> or was previously marked as injected.")
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("HTML rendering error: %w", err)
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
		// Explicitly check for data: URI scheme here
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

func generateCSP(prefs sitePreferences, targetURL *url.URL, clientReq *http.Request) string {
	directives := map[string]string{
		"default-src": "'none'",
		"object-src":  "'none'",
		"base-uri":    "'self'", 
		"form-action": "'self'", 
	}

	scriptSrc := []string{"'self'"} 
	if prefs.JavaScriptEnabled {
		scriptSrc = append(scriptSrc, "'unsafe-inline'", "'unsafe-eval'") 
	}
	directives["script-src"] = strings.Join(scriptSrc, " ")
	directives["worker-src"] = "'self'" 

	// Allow inline styles for the injected home button and potentially from the target site
	styleSrc := []string{"'self'", "'unsafe-inline'"} 
	directives["style-src"] = strings.Join(styleSrc, " ")

	imgSrc := []string{"'self'", "data:", "blob:"} 
	directives["img-src"] = strings.Join(imgSrc, " ")

	fontSrc := []string{"'self'", "data:"}
	directives["font-src"] = strings.Join(fontSrc, " ")

	connectSrc := []string{"'self'"} 
	if prefs.JavaScriptEnabled {
	}
	directives["connect-src"] = strings.Join(connectSrc, " ")
	
	if prefs.IframesEnabled {
		directives["frame-src"] = "'self' data: blob:" 
	} else {
		directives["frame-src"] = "'none'"
	}
	directives["child-src"] = directives["frame-src"] 

	mediaSrc := []string{"'self'", "blob:"}
	directives["media-src"] = strings.Join(mediaSrc, " ")
	
	manifestSrc := []string{"'self'"}
	directives["manifest-src"] = strings.Join(manifestSrc, " ")

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// CSP for landing page needs to allow blob: for the embedded Service Worker registration
	w.Header().Set("Content-Security-Policy", "default-src 'self' blob:; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; object-src 'none'; base-uri 'self'; form-action 'self';")
	fmt.Fprint(w, makeLandingPageHTML()) 
}

func setupOutgoingHeadersForProxy(proxyToTargetReq *http.Request, clientToProxyReq *http.Request, targetHost string, prefs sitePreferences) {
	proxyToTargetReq.Header.Set("Host", targetHost)
	proxyToTargetReq.Header.Set("User-Agent", defaultUserAgent) 
	proxyToTargetReq.Header.Set("Accept", clientToProxyReq.Header.Get("Accept"))
	proxyToTargetReq.Header.Set("Accept-Language", clientToProxyReq.Header.Get("Accept-Language"))
	proxyToTargetReq.Header.Del("Accept-Encoding") 

	proxyToTargetReq.Header.Del("Cookie") 
	if prefs.CookiesEnabled {
		var cookiesToSend []string
		for _, cookie := range clientToProxyReq.Cookies() {
			if cookie.Name == authCookieName || 
				strings.HasPrefix(cookie.Name, "proxy-") { // UPDATED to filter "proxy-" prefix
				continue
			}
			cookiesToSend = append(cookiesToSend, cookie.Name+"="+cookie.Value)
		}
		if len(cookiesToSend) > 0 {
			proxyToTargetReq.Header.Set("Cookie", strings.Join(cookiesToSend, "; "))
			log.Printf("Cookies enabled: Forwarding %d filtered cookies to %s", len(cookiesToSend), targetHost)
		}
	} else {
		log.Printf("Cookies disabled: Not forwarding any cookies to %s", targetHost)
	}

	proxyToTargetReq.Header.Del("X-Forwarded-For")
	proxyToTargetReq.Header.Del("X-Real-Ip")
	proxyToTargetReq.Header.Del("Forwarded")
	proxyToTargetReq.Header.Del("Via")
}


func handleProxyContent(w http.ResponseWriter, r *http.Request) {
	targetURLString := r.URL.Query().Get("url")
	if targetURLString == "" {
		http.Error(w, "Missing 'url' query parameter for proxy", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(targetURLString, "http://") && !strings.HasPrefix(targetURLString, "https://") {
		// Attempt to prepend http:// if no scheme is present. This is a basic fix.
		// A more robust solution might involve more sophisticated URL parsing/validation.
		log.Printf("Warning: Target URL '%s' missing scheme, prepending http://", targetURLString)
		targetURLString = "http://" + targetURLString
	}
	targetURL, err := url.Parse(targetURLString)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.Host == "" {
		http.Error(w, "Invalid target URL for proxy: "+targetURLString+" Error: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Construct sitePreferences directly here
	prefs := sitePreferences{
		JavaScriptEnabled: getBoolCookie(r, "proxy-js-enabled"),
		CookiesEnabled:    getBoolCookie(r, "proxy-cookies-enabled"),
		IframesEnabled:    getBoolCookie(r, "proxy-iframes-enabled"),
	}
	log.Printf("handleProxyContent: Proxying for %s. JS:%t, Cookies:%t, Iframes:%t",
		targetURL.String(), prefs.JavaScriptEnabled, prefs.CookiesEnabled, prefs.IframesEnabled)

	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body) 
	if err != nil {
		http.Error(w, "Error creating target request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Pass the locally constructed prefs struct
	setupOutgoingHeadersForProxy(proxyReq, r, targetURL.Host, prefs)


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
			(lowerName == "content-encoding" && targetResp.Header.Get("Content-Encoding") == "gzip") { 
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

	w.Header().Set("Content-Security-Policy", generateCSP(prefs, targetURL, r))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-XSS-Protection", "0") 
	w.Header().Set("Referrer-Policy", "no-referrer-when-downgrade") 
	w.Header().Set("X-Proxy-Version", "GoPrivacyProxy-v2-embedded")


	bodyBytes, wasGzipped, err := readAndDecompressBody(targetResp)
	if err != nil {
		http.Error(w, "Error reading/decompressing target body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if wasGzipped {
		w.Header().Del("Content-Encoding") 
		w.Header().Del("Content-Length")   
	}

	contentType := targetResp.Header.Get("Content-Type")
	isHTML := strings.HasPrefix(contentType, "text/html")
	isCSS := strings.HasPrefix(contentType, "text/css")
	isSuccess := targetResp.StatusCode >= 200 && targetResp.StatusCode < 300

	if isSuccess {
		if isHTML {
			rewrittenHTMLReader, errRewrite := rewriteHTMLContentAdvanced(bytes.NewReader(bodyBytes), targetURL, r, prefs)
			if errRewrite != nil {
				log.Printf("Error rewriting HTML for %s: %v. Serving original.", targetURL.String(), errRewrite)
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
		} else if strings.Contains(contentType, "javascript") && !prefs.JavaScriptEnabled {
			log.Printf("Blocking JavaScript content from %s due to privacy setting (JS disabled).", targetURL.Host)
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.WriteHeader(http.StatusForbidden) 
			fmt.Fprint(w, "// JavaScript execution disabled by proxy for this site.")
			return
		}
	}

	if !wasGzipped { 
		if cl := targetResp.Header.Get("Content-Length"); cl != "" {
			w.Header().Set("Content-Length", cl)
		}
	} else {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes))) 
	}
	w.WriteHeader(targetResp.StatusCode)
	w.Write(bodyBytes)
}


// --- Master Handler (Auth Gatekeeper & Router) ---
func masterHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("masterHandler: Path %s, Method: %s", r.URL.Path, r.Method)

	// Auth check for all paths except /auth/*
	// Static assets are now embedded, so no /static/* bypass needed here.
	if !strings.HasPrefix(r.URL.Path, "/auth/") {
		isValidAuth, _, validationErr := isCFAuthCookieValid(r)
		if validationErr != nil {
			log.Printf("CF_Authorization cookie validation error for %s: %v. Auth required.", r.URL.Path, validationErr)
		}

		if !isValidAuth {
			isLikelyHTMLRequest := strings.Contains(r.Header.Get("Accept"), "text/html") ||
				r.Header.Get("Accept") == "" || r.Header.Get("Accept") == "*/*"

			if r.Method == http.MethodGet && (r.URL.Path == "/" || (isLikelyHTMLRequest && r.URL.Path != proxyRequestPath)) {
				log.Printf("CF_Authorization invalid/missing for %s. Redirecting to /auth/enter-email.", r.URL.Path)
				originalURL := r.URL.RequestURI() 
				http.SetCookie(w, &http.Cookie{
					Name:     originalURLCookieName,
					Value:    url.QueryEscape(originalURL),
					Path:     "/", 
					HttpOnly: true,
					Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
					SameSite: http.SameSiteLaxMode,
					MaxAge:   300, 
				})
				http.Redirect(w, r, "/auth/enter-email", http.StatusFound)
				return
			} else { 
				log.Printf("CF_Authorization invalid/missing for %s %s. Returning 401.", r.Method, r.URL.Path)
				http.Error(w, "Unauthorized: Authentication required.", http.StatusUnauthorized)
				return
			}
		}
	}

	if r.URL.Path == "/" {
		handleLandingPage(w, r)
	} else if r.URL.Path == proxyRequestPath { 
		handleProxyContent(w, r)
	} else if !strings.HasPrefix(r.URL.Path, "/auth/") { 
		// If it's not root, not /proxy, and not /auth/, then it's a 404.
		// Static asset paths like /static/style.css are no longer valid routes.
		http.NotFound(w, r)
	}
	// Auth paths like /auth/enter-email are handled by their specific http.HandleFunc calls in main.
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
