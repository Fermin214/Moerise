export function formSignature(form) {
  return JSON.stringify([...form.querySelectorAll('input,select,textarea')]
    .filter(input => !input.matches('[data-language], [data-delivery-receipt]'))
    .map(input => [input.id || input.name, input.type === 'checkbox' ? input.checked : input.value]));
}
