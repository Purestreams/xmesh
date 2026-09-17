document.querySelectorAll('button[data-copy]').forEach((button) => {
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(button.dataset.copy);
      const before = button.textContent;
      button.textContent = 'Copied';
      setTimeout(() => { button.textContent = before; }, 2000);
    } catch {
      button.previousElementSibling?.select();
    }
  });
});

document.getElementById('refresh-status')?.addEventListener('click', () => location.reload());
document.addEventListener('input', (event) => {
  event.target.closest('form')?.setAttribute('data-dirty', 'true');
});
setInterval(() => {
  if (document.visibilityState !== 'visible' || document.querySelector('details[open], form[data-dirty]')) return;
  const active = document.activeElement;
  if (active?.matches('input, select, textarea')) return;
  location.reload();
}, 30000);
