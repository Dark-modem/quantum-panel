// Matrix settings use a separate modal from setup and delete confirmation.
let matrixEditState = null;
window.matrixEditPanelOpen = async function() {
  const st = await api('/api/matrix/status');
  if (!st.enabled) return;
  matrixEditState = st;
  const body = $('matrixEditBody');
  const help = text => esc(tr('', text));
  $('matrixEditTitle').textContent = tr('', 'Edit Matrix tunnel');
  if (st.role === 'hub') {
    body.innerHTML = `<div class="fg"><label for="mxEditPorts">${help('Forwarded ports (comma list)')}</label><input id="mxEditPorts" class="hashem-input u-mono" dir="ltr" value="${esc((st.ports || []).join(', '))}"></div>
      <div class="fg"><label id="mxEditFamilyLabel">${help('Family')}</label><div id="mxEditFamily" class="qs-seg mx-family-seg" role="radiogroup" aria-labelledby="mxEditFamilyLabel" dir="ltr">
        <button type="button" role="radio" data-mx-edit-family="ipv4" aria-checked="${st.family === 'ipv4'}" tabindex="${st.family === 'ipv4' ? 0 : -1}">IPv4</button>
        <button type="button" role="radio" data-mx-edit-family="ipv6" aria-checked="${st.family === 'ipv6'}" tabindex="${st.family === 'ipv6' ? 0 : -1}">IPv6</button></div></div>
      <div class="fg"><label for="mxEditAddr">${help('Hub transport address')}</label><input id="mxEditAddr" class="hashem-input u-mono" dir="ltr" value="${esc(st.addr || '')}" placeholder="auto"></div>
      <p class="qs-hint">${help('Port edits preserve tunnel keys. Changing IPv4/IPv6 interrupts the transport until the updated bundle is applied on the other server.')}</p>`;
    matrixEditState.family = st.family;
    body.querySelectorAll('[data-mx-edit-family]').forEach(b => {
      b.onclick = () => matrixEditFamily(b.dataset.mxEditFamily);
      b.onkeydown = e => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
        e.preventDefault();
        const family = e.key === 'Home' ? 'ipv4' : e.key === 'End' ? 'ipv6' : matrixEditState.family === 'ipv4' ? 'ipv6' : 'ipv4';
        matrixEditFamily(family);
        body.querySelector(`[data-mx-edit-family="${family}"]`).focus();
      };
    });
  } else {
    body.innerHTML = `<p class="qs-hint">${help('Edit settings on the Iran hub first. Paste its updated Matrix bundle here; existing keys and node identity are kept.')}</p>
      <div class="fg"><label for="mxEditBundle">${help('Updated Matrix bundle')}</label><textarea id="mxEditBundle" rows="4" class="hashem-input u-mono" dir="ltr" placeholder="hmx1_…" spellcheck="false" autocomplete="off"></textarea></div>
      <hr>
      <b>${help('Connect a local service to Matrix')}</b>
      <p class="qs-hint">${help('For Xray listening only on 127.0.0.1:443, connect Matrix port 443 to local port 443. TLS/Reality keys and Xray configuration stay unchanged. This bridge supports TCP only.')}</p>
      <div class="fg"><label for="mxLocalPort">${help('Matrix user port')}</label><input class="hashem-input u-mono" id="mxLocalPort" inputmode="numeric" value="${esc(String((st.ports || [443])[0] || 443))}" dir="ltr"></div>
      <div class="fg"><label for="mxLocalTarget">${help('Local service port')}</label><input class="hashem-input u-mono" id="mxLocalTarget" inputmode="numeric" value="443" dir="ltr"></div>
      <button type="button" class="hashem-btn hashem-btn-primary" id="mxConnectLocal">${help('Connect local service')}</button>
      <button type="button" class="hashem-btn hashem-btn-ghost" id="mxDisconnectLocal">${help('Remove local bridge')}</button>
      <div class="qs-hint u-mt-8" id="mxLocalStatus" role="status" aria-live="polite"></div>`;
    $('mxConnectLocal').onclick = () => matrixLocalServiceSave(false);
    $('mxDisconnectLocal').onclick = () => matrixLocalServiceSave(true);
    const links = await api('/api/matrix/local-service');
    $('mxLocalStatus').textContent = (links.services || []).map(s => s.port + ' → 127.0.0.1:' + s.target_port).join(' · ');
  }
  $('matrixEditMsg').textContent = '';
  $('matrixEditSave').disabled = false;
  modalShow($('matrixEditModal'), matrixEditClose);
}
async function matrixLocalServiceSave(remove) {
  const status = $('mxLocalStatus'), connect = $('mxConnectLocal'), disconnect = $('mxDisconnectLocal');
  const port = Number($('mxLocalPort').value), target = Number($('mxLocalTarget').value);
  connect.disabled = disconnect.disabled = true;
  status.textContent = tr('', 'Connecting local service…');
  try {
    await api('/api/matrix/local-service', {port, target_port: target, remove});
    status.textContent = tr('', remove ? 'Local bridge removed.' : 'Local service connected to Matrix. Xray configuration unchanged.');
  } catch(e) { status.textContent = e.message; }
  finally { connect.disabled = disconnect.disabled = false; }
}
function matrixEditFamily(family) {
  if (!matrixEditState || !['ipv4', 'ipv6'].includes(family)) return;
  if (matrixEditState.family !== family) $('mxEditAddr').value = '';
  matrixEditState.family = family;
  $('matrixEditBody').querySelectorAll('[data-mx-edit-family]').forEach(b => {
    const on = b.dataset.mxEditFamily === family;
    b.setAttribute('aria-checked', String(on)); b.tabIndex = on ? 0 : -1;
  });
}
function matrixEditClose() { modalHide($('matrixEditModal')); matrixEditState = null; }
async function matrixEditSave() {
  if (!matrixEditState) return;
  const btn = $('matrixEditSave'), msg = $('matrixEditMsg');
  const role = matrixEditState.role;
  let payload;
  if (role === 'hub') {
    const raw = ($('mxEditPorts').value || '').split(/[ ,]+/).filter(Boolean);
    if (!raw.length || raw.some(v => !/^\d+$/.test(v))) { msg.textContent = tr('', 'Enter valid comma-separated port numbers.'); return; }
    payload = { ports: raw.map(Number), family: matrixEditState.family, addr: $('mxEditAddr').value.trim() };
  } else {
    payload = { bundle: $('mxEditBundle').value.trim() };
    if (!payload.bundle) { msg.textContent = tr('', 'Updated Matrix bundle is required.'); return; }
  }
  btn.disabled = true;
  msg.textContent = tr('', 'Saving Matrix settings…');
  try {
    const r = await api('/api/matrix/edit', payload);
    await loadMatrix();
    if (role === 'hub' && r.bundle) {
      $('matrixEditBody').innerHTML = `<p class="qs-hint">${esc(tr('', r.peer_sync_required
        ? 'Hub settings saved. Apply this updated bundle in Edit on the foreign server to finish the transport switch.'
        : 'Settings saved. This bundle updates the displayed settings on the foreign server too.'))}</p>
        <div class="fg"><label for="mxEditedBundle">${esc(tr('', 'Updated Matrix bundle'))}</label><textarea id="mxEditedBundle" rows="5" readonly class="hashem-input u-mono" dir="ltr">${esc(r.bundle)}</textarea></div>`;
      msg.textContent = tr('', 'Keys and node identity preserved.');
    } else {
      matrixEditClose(); toastOk(tr('', 'Matrix settings updated'));
    }
    if (typeof refresh === 'function') refresh();
  } catch (e) {
    msg.textContent = e.message;
    btn.disabled = false;
  }
}
$('matrixEditSave').onclick = matrixEditSave;
$('matrixEditCancel').onclick = $('matrixEditX').onclick = matrixEditClose;
$('matrixEditModal').onclick = e => { if (e.target === $('matrixEditModal')) matrixEditClose(); };
