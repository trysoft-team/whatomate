import { test, expect } from '@playwright/test'
import { LoginPage } from '../../pages'
import { TEST_USERS, logout } from '../../helpers'

test.describe('Login', () => {
  let loginPage: LoginPage

  test.beforeEach(async ({ page }) => {
    loginPage = new LoginPage(page)
    await loginPage.goto()
  })

  test('should display the email step first', async ({ page }) => {
    await expect(loginPage.emailInput).toBeVisible()
    await expect(loginPage.passwordInput).toHaveCount(0)
    await expect(loginPage.submitButton).toBeVisible()
  })

  test('should login successfully with valid credentials', async ({ page }) => {
    await loginPage.login(TEST_USERS.admin.email, TEST_USERS.admin.password)
    await loginPage.expectLoginSuccess()
  })

  test('should show error with invalid credentials', async ({ page }) => {
    await loginPage.login('invalid@test.com', 'wrongpassword')
    await loginPage.expectLoginError()
  })

  test('should keep Continue disabled until the email is valid', async ({ page }) => {
    await expect(loginPage.submitButton).toBeDisabled()
    await loginPage.emailInput.fill('not-an-email')
    await expect(loginPage.submitButton).toBeDisabled()
    await loginPage.emailInput.fill('test@test.com')
    await expect(loginPage.submitButton).toBeEnabled()
  })

  test('should ask for the password after Continue', async ({ page }) => {
    await loginPage.emailInput.fill('test@test.com')
    await loginPage.submitButton.click()
    await expect(loginPage.passwordInput).toBeVisible()
    await expect(page.getByTestId('login-email-display')).toHaveText('test@test.com')
    // Sign in stays disabled until a password is typed
    await expect(loginPage.submitButton).toBeDisabled()
    await expect(page).toHaveURL(/\/login/)

    await page.getByTestId('login-change-email').click()
    await expect(loginPage.emailInput).toHaveValue('test@test.com')
    await expect(loginPage.passwordInput).toHaveCount(0)
  })

  test('should navigate to register page', async ({ page }) => {
    await loginPage.goToRegister()
    await expect(page).toHaveURL(/\/register/)
  })

  test('should logout successfully', async ({ page }) => {
    // First login
    await loginPage.login(TEST_USERS.admin.email, TEST_USERS.admin.password)
    await loginPage.expectLoginSuccess()

    // Then logout
    await logout(page)
    await expect(page).toHaveURL(/\/login/)
  })
})

test.describe('Authentication Redirect', () => {
  test('should redirect to login when accessing protected route', async ({ page }) => {
    await page.goto('/settings/users')
    await expect(page).toHaveURL(/\/login/)
  })

  test('should redirect to dashboard after login', async ({ page }) => {
    const loginPage = new LoginPage(page)
    await loginPage.goto()
    await loginPage.login(TEST_USERS.admin.email, TEST_USERS.admin.password)
    // Should be on dashboard or chat
    await expect(page).toHaveURL(/\/(dashboard|chat)?/)
  })

  test('should redirect authenticated user away from login page', async ({ page }) => {
    const loginPage = new LoginPage(page)
    await loginPage.goto()
    await loginPage.login(TEST_USERS.admin.email, TEST_USERS.admin.password)
    await loginPage.expectLoginSuccess()
    
    await page.goto('/login')
    await expect(page).not.toHaveURL(/\/login/)
  })

  test('should redirect authenticated user away from register page', async ({ page }) => {
    const loginPage = new LoginPage(page)
    await loginPage.goto()
    await loginPage.login(TEST_USERS.admin.email, TEST_USERS.admin.password)
    await loginPage.expectLoginSuccess()
    
    await page.goto('/register')
    await expect(page).not.toHaveURL(/\/register/)
  })
})
