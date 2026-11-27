(function () {
  var el = document.getElementById("gowa-status");
  if (!el) return;
  fetch("/api/v1/system/health")
    .then(function (r) { return r.json(); })
    .then(function (data) {
      el.textContent = "Engine: " + (data.engine || "whatsmeow") + " | Devices: " + (data.devices_total || 0);
    })
    .catch(function () {
      el.textContent = "Gateway unavailable";
    });
})();
