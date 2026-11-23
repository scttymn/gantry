// The app's JavaScript, loaded through the import map (assets/assets.go) with
// no build step: Turbo for links, forms and live updates, and Stimulus for
// behaviour, each controller in js/controllers registered by its name
// (hello_controller.js is data-controller="hello", admin/tabs_controller.js
// is "admin--tabs"). `gantry importmap pin PACKAGE` adds a package.
import "@hotwired/turbo"
import { Application } from "@hotwired/stimulus"

const application = Application.start()
window.Stimulus = application

const { imports } = JSON.parse(document.querySelector("script[type=importmap]").textContent)
for (const name of Object.keys(imports)) {
  const match = name.match(/^controllers\/(.+)_controller$/)
  if (match) {
    const identifier = match[1].replaceAll("/", "--").replaceAll("_", "-")
    import(name).then((module) => application.register(identifier, module.default))
  }
}
