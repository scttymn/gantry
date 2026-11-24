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
