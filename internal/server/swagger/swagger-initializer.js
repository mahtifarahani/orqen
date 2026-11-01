window.onload = function () {
  window.ui = SwaggerUIBundle({url: '/api/v1/openapi.json', dom_id: '#swagger-ui', presets: [SwaggerUIBundle.presets.apis]})
}
