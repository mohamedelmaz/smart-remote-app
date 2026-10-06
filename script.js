document.addEventListener('DOMContentLoaded', () => {
    const langToggle = document.getElementById('lang-toggle');
    const html = document.documentElement;
    
    // Check for saved language preference or default to English
    let currentLang = localStorage.getItem('smart-remote-lang') || 'en';
    setLanguage(currentLang);

    langToggle.addEventListener('click', () => {
        currentLang = currentLang === 'en' ? 'ar' : 'en';
        localStorage.setItem('smart-remote-lang', currentLang);
        setLanguage(currentLang);
    });

    function setLanguage(lang) {
        if (lang === 'ar') {
            html.setAttribute('dir', 'rtl');
            html.setAttribute('lang', 'ar');
            langToggle.textContent = 'English';
        } else {
            html.setAttribute('dir', 'ltr');
            html.setAttribute('lang', 'en');
            langToggle.textContent = 'العربية';
        }

        // Update all text elements with data attributes
        document.querySelectorAll('[data-en][data-ar]').forEach(el => {
            el.textContent = el.getAttribute(`data-${lang}`);
        });
    }

    // Disable context menu for professional app feel
    document.addEventListener('contextmenu', e => e.preventDefault());
});