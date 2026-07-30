document.addEventListener('DOMContentLoaded', async () => {
    try {
        const response = await fetch('/api/auth/me', {
            headers: { Accept: 'application/json' },
            cache: 'no-store'
        });
        if (response.ok) {
            window.location.replace('/');
            return;
        }
    } catch {
        // The form below gives the useful connection error on submit.
    }

    document.getElementById('loginForm').addEventListener('submit', signIn);
});

async function signIn(event) {
    event.preventDefault();
    const button = document.getElementById('loginButton');
    const message = document.getElementById('loginMessage');
    button.disabled = true;
    message.textContent = '';

    try {
        const response = await fetch('/api/auth/login', {
            method: 'POST',
            headers: {
                Accept: 'application/json',
                'Content-Type': 'application/json'
            },
            cache: 'no-store',
            body: JSON.stringify({
                email: document.getElementById('login').value,
                password: document.getElementById('password').value
            })
        });
        const payload = await response.json();
        if (!response.ok) {
            message.textContent = payload.error || 'Sign-in failed.';
            return;
        }
        window.location.replace('/');
    } catch (error) {
        console.error('Sign-in failed:', error);
        message.textContent = 'Sentinel is unavailable. Check the service and try again.';
    } finally {
        button.disabled = false;
    }
}
